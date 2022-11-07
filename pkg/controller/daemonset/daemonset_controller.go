/*
Copyright 2022 The Sonic_k8s Authors.
Copyright 2015 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package daemonset

import (
	"context"
	"flag"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/flowcontrol"
	v1helper "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
	kubecontroller "k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/daemon/util"
	daemonsetutil "k8s.io/kubernetes/pkg/controller/daemon/util"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	appspub "github.com/sonic-net/sonic-k8s-operator/apis/apps/pub"

	"k8s.io/client-go/kubernetes/scheme"

	"github.com/sonic-net/sonic-k8s-operator/pkg/client"
	ctrutil "github.com/sonic-net/sonic-k8s-operator/pkg/util"
	utilclient "github.com/sonic-net/sonic-k8s-operator/pkg/util/client"
	ctrExpectations "github.com/sonic-net/sonic-k8s-operator/pkg/util/expectations"
	"github.com/sonic-net/sonic-k8s-operator/pkg/util/ratelimiter"
	"github.com/sonic-net/sonic-k8s-operator/pkg/util/requeueduration"
	"github.com/sonic-net/sonic-k8s-operator/pkg/util/revisionadapter"
)

func init() {
	flag.BoolVar(&scheduleDaemonSetPods, "assign-pods-by-scheduler", true, "Use scheduler to assign pod to node.")
	flag.IntVar(&concurrentReconciles, "daemonset-workers", concurrentReconciles, "Max concurrent workers for DaemonSet controller.")
}

var (
	concurrentReconciles  = 3
	scheduleDaemonSetPods bool

	// controllerKind contains the schema.GroupVersionKind for this controller type.
	controllerKind = apps.SchemeGroupVersion.WithKind("DaemonSet")

	onceBackoffGC sync.Once
	// this is a short cut for any sub-functions to notify the reconcile how long to wait to requeue
	durationStore = requeueduration.DurationStore{}

	isPreDownloadDisabled bool
)

const (
	// BurstReplicas is a rate limiter for booting pods on a lot of pods.
	// The value of 250 is chosen b/c values that are too high can cause registry DoS issues.
	BurstReplicas = 250

	// BackoffGCInterval is the time that has to pass before next iteration of backoff GC is run
	BackoffGCInterval = 1 * time.Minute
)

// Reasons for DaemonSet events
const (
	// SelectingAllReason is added to an event when a DaemonSet selects all Pods.
	SelectingAllReason = "SelectingAll"
	// FailedPlacementReason is added to an event when a DaemonSet can't schedule a Pod to a specified node.
	FailedPlacementReason = "FailedPlacement"
	// FailedDaemonPodReason is added to an event when the status of a Pod of a DaemonSet is 'Failed'.
	FailedDaemonPodReason = "FailedDaemonPod"
)

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new DaemonSet Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	r, err := newReconciler(mgr)
	if err != nil {
		return err
	}
	return add(mgr, r)
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) (reconcile.Reconciler, error) {
	genericClient := client.GetGenericClientWithName("daemonset-controller")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: genericClient.KubeClient.CoreV1().Events("")})

	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "sonic-daemonset-controller"})
	cacher := mgr.GetCache()

	dsInformer, err := cacher.GetInformerForKind(context.TODO(), apps.SchemeGroupVersion.WithKind("DaemonSet"))
	if err != nil {
		return nil, err
	}
	podInformer, err := cacher.GetInformerForKind(context.TODO(), corev1.SchemeGroupVersion.WithKind("Pod"))
	if err != nil {
		return nil, err
	}
	nodeInformer, err := cacher.GetInformerForKind(context.TODO(), corev1.SchemeGroupVersion.WithKind("Node"))
	if err != nil {
		return nil, err
	}
	revInformer, err := cacher.GetInformerForKind(context.TODO(), apps.SchemeGroupVersion.WithKind("ControllerRevision"))
	if err != nil {
		return nil, err
	}

	dsLister := appslisters.NewDaemonSetLister(dsInformer.(cache.SharedIndexInformer).GetIndexer())
	historyLister := appslisters.NewControllerRevisionLister(revInformer.(cache.SharedIndexInformer).GetIndexer())
	podLister := corelisters.NewPodLister(podInformer.(cache.SharedIndexInformer).GetIndexer())
	nodeLister := corelisters.NewNodeLister(nodeInformer.(cache.SharedIndexInformer).GetIndexer())
	failedPodsBackoff := flowcontrol.NewBackOff(1*time.Second, 15*time.Minute)
	revisionAdapter := revisionadapter.NewDefaultImpl()

	cli := utilclient.NewClientFromManager(mgr, "daemonset-controller")
	dsc := &ReconcileDaemonSet{
		Client:        cli,
		kubeClient:    genericClient.KubeClient,
		eventRecorder: recorder,
		podControl:    kubecontroller.RealPodControl{KubeClient: genericClient.KubeClient, Recorder: recorder},
		crControl: kubecontroller.RealControllerRevisionControl{
			KubeClient: genericClient.KubeClient,
		},
		expectations:                kubecontroller.NewControllerExpectations(),
		resourceVersionExpectations: ctrExpectations.NewResourceVersionExpectation(),
		dsLister:                    dsLister,
		historyLister:               historyLister,
		podLister:                   podLister,
		nodeLister:                  nodeLister,
		failedPodsBackoff:           failedPodsBackoff,
		revisionAdapter:             revisionAdapter,
	}
	return dsc, err
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("daemonset-controller", mgr, controller.Options{
		Reconciler: r, MaxConcurrentReconciles: concurrentReconciles,
		RateLimiter: ratelimiter.DefaultControllerRateLimiter()})
	if err != nil {
		return err
	}

	dsc := r.(*ReconcileDaemonSet)

	// Watch for changes to DaemonSet
	err = c.Watch(&source.Kind{Type: &apps.DaemonSet{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ds := e.Object.(*apps.DaemonSet)
			klog.V(4).Infof("Adding DaemonSet %s/%s", ds.Namespace, ds.Name)
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldDS := e.ObjectOld.(*apps.DaemonSet)
			newDS := e.ObjectNew.(*apps.DaemonSet)
			if oldDS.UID != newDS.UID {
				dsc.expectations.DeleteExpectations(keyFunc(oldDS))
			}

			// check DeploymentPaused
			isPaused := false
			if DeploymentPaused, found := newDS.Annotations[string(appspub.DaemonSetDeploymentPausedKey)]; found {
				if DeploymentPaused == "true" {
					klog.V(4).Infof("DaemonSet %s/%s is paused, skip update", newDS.Namespace, newDS.Name)
					isPaused = true
				}
			}

			if newDS.Annotations[string(appspub.DaemonSetDeploymentPausedKey)] != oldDS.Annotations[string(appspub.DaemonSetDeploymentPausedKey)] {
				return true
			}

			if !isPaused && oldDS.Spec.Template.Spec.Containers[0].Image == newDS.Spec.Template.Spec.Containers[0].Image {
				klog.V(4).Infof("Updating DaemonSet %s/%s, no container change, skip", newDS.Namespace, newDS.Name)
				return false
			}

			klog.V(4).Infof("Updating DaemonSet %s/%s", newDS.Namespace, newDS.Name)
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			ds := e.Object.(*apps.DaemonSet)
			klog.V(4).Infof("Deleting DaemonSet %s/%s", ds.Namespace, ds.Name)
			dsc.expectations.DeleteExpectations(keyFunc(ds))
			newPodForDSCache.Delete(ds.UID)
			return true
		},
	})
	if err != nil {
		return err
	}

	// Watch for changes to Node.
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &nodeEventHandler{reader: mgr.GetCache()})
	if err != nil {
		return err
	}

	// Watch for changes to Pod created by DaemonSet
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &podEventHandler{Reader: mgr.GetCache()})
	if err != nil {
		return err
	}

	klog.V(4).Info("finished to add daemonset-controller")
	return nil
}

var _ reconcile.Reconciler = &ReconcileDaemonSet{}

// ReconcileDaemonSet reconciles a DaemonSet object
type ReconcileDaemonSet struct {
	runtimeclient.Client
	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder
	podControl    kubecontroller.PodControlInterface
	crControl     kubecontroller.ControllerRevisionControlInterface

	// A TTLCache of pod creates/deletes each ds expects to see
	expectations kubecontroller.ControllerExpectationsInterface
	// A cache of pod resourceVersion expecatations
	resourceVersionExpectations ctrExpectations.ResourceVersionExpectation

	// dsLister can list/get daemonsets from the shared informer's store
	dsLister appslisters.DaemonSetLister
	// historyLister get list/get history from the shared informers's store
	historyLister appslisters.ControllerRevisionLister
	// podLister get list/get pods from the shared informers's store
	podLister corelisters.PodLister
	// nodeLister can list/get nodes from the shared informer's store
	nodeLister corelisters.NodeLister

	failedPodsBackoff *flowcontrol.Backoff

	revisionAdapter revisionadapter.Interface
}

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets/status,verbs=get;update;patch

// Reconcile reads that state of the cluster for a DaemonSet object and makes changes based on the state read
// and what is in the DaemonSet.Spec
func (dsc *ReconcileDaemonSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, retErr error) {
	onceBackoffGC.Do(func() {
		go wait.Until(dsc.failedPodsBackoff.GC, BackoffGCInterval, ctx.Done())
	})
	startTime := time.Now()
	defer func() {
		if retErr == nil {
			if res.Requeue || res.RequeueAfter > 0 {
				klog.Infof("Finished syncing DaemonSet %s, cost %v, result: %v", request, time.Since(startTime), res)
			} else {
				klog.Infof("Finished syncing DaemonSet %s, cost %v", request, time.Since(startTime))
			}
		} else {
			klog.Errorf("Failed syncing DaemonSet %s: %v", request, retErr)
		}
		// clean the duration store
		_ = durationStore.Pop(request.String())
	}()

	err := dsc.syncDaemonSet(request)
	return reconcile.Result{RequeueAfter: durationStore.Pop(request.String())}, err
}

// getDaemonPods returns daemon pods owned by the given ds.
// This also reconciles ControllerRef by adopting/orphaning.
// Note that returned Pods are pointers to objects in the cache.
// If you want to modify one, you need to deep-copy it first.
func (dsc *ReconcileDaemonSet) getDaemonPods(ds *apps.DaemonSet) ([]*corev1.Pod, error) {
	selector, err := ctrutil.ValidatedLabelSelectorAsSelector(ds.Spec.Selector)
	if err != nil {
		return nil, err
	}

	// List all pods to include those that don't match the selector anymore but
	// have a ControllerRef pointing to this controller.
	pods, err := dsc.podLister.Pods(ds.Namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	// If any adoptions are attempted, we should first recheck for deletion with
	// an uncached quorum read sometime after listing Pods (see #42639).
	dsNotDeleted := kubecontroller.RecheckDeletionTimestamp(func() (metav1.Object, error) {
		fresh, err := dsc.kubeClient.AppsV1().DaemonSets(ds.Namespace).Get(context.TODO(), ds.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if fresh.UID != ds.UID {
			return nil, fmt.Errorf("original DaemonSet %v/%v is gone: got uid %v, wanted %v", ds.Namespace, ds.Name, fresh.UID, ds.UID)
		}
		return fresh, nil
	})

	// Use ControllerRefManager to adopt/orphan as needed.
	cm := kubecontroller.NewPodControllerRefManager(dsc.podControl, ds, selector, controllerKind, dsNotDeleted)
	return cm.ClaimPods(pods)
}

func (dsc *ReconcileDaemonSet) syncDaemonSet(request reconcile.Request) error {
	dsKey := request.NamespacedName.String()
	klog.Infof("Start sync DaemonSet %v", dsKey)

	ds, err := dsc.dsLister.DaemonSets(request.Namespace).Get(request.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(4).Infof("DaemonSet has been deleted %s", dsKey)
			dsc.expectations.DeleteExpectations(dsKey)
			return nil
		}
		return fmt.Errorf("unable to retrieve DaemonSet %s from store: %v", dsKey, err)
	}

	// check deamonset update type, we only handle OnDelete
	if ds.Spec.UpdateStrategy.Type != apps.OnDeleteDaemonSetStrategyType {
		klog.V(4).Infof("DaemonSet %s UpdateStrategy is not OnDelete, skip it.", dsKey)
		return nil
	}

	// Don't process a daemon set until all its creations and deletions have been processed.
	// For example if daemon set foo asked for 3 new daemon pods in the previous call to manage,
	// then we do not want to call manage on foo until the daemon pods have been created.
	if ds.DeletionTimestamp != nil {
		return nil
	}

	everything := metav1.LabelSelector{}
	if reflect.DeepEqual(ds.Spec.Selector, &everything) {
		dsc.eventRecorder.Eventf(ds, corev1.EventTypeWarning, SelectingAllReason, "This DaemonSet is selecting all pods. A non-empty selector is required.")
		return nil
	}

	nodeList, err := dsc.nodeLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("Couldn't get list of nodes when syncing DaemonSet %#v: %v", ds, err)
	}
	klog.Infof("Found %v node for ds %s/%s", len(nodeList), ds.Namespace, ds.Name)

	checkOrUpdateRollbackLock(fmt.Sprintf("%s/%s", ds.Namespace, ds.Name))

	curVersion, _ := dsc.getCurrentDsVersion(ds)
	if curVersion == nil {
		klog.V(4).Infof("Failed to get deamonset version for %s/%s, will try it later.", ds.Namespace, ds.Name)
		durationStore.Push(keyFunc(ds), time.Duration(5)*time.Second)
		return nil
	}

	hash := curVersion.Labels[apps.DefaultDaemonSetUniqueLabelKey]
	klog.Infof("Check rollback for %s/%s", ds.Namespace, ds.Name)
	dsc.rollback(ds, nodeList, hash)
	if err != nil {
		klog.Infof("Rollback fail for %s/%s", ds.Namespace, ds.Name)
		return err
	}

	// refresh ds after rollback
	ds, err = dsc.dsLister.DaemonSets(request.Namespace).Get(request.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(4).Infof("DaemonSet has been deleted %s", dsKey)
			dsc.expectations.DeleteExpectations(dsKey)
			return nil
		}
		return fmt.Errorf("unable to retrieve DaemonSet %s from store: %v", dsKey, err)
	}

	if isDaemonSetPaused(ds) {
		klog.Infof("Daemonset %s/%s is paused, skip...", ds.Namespace, ds.Name)
		return nil
	}

	klog.Infof("Get ds hash %v for %s/%s", hash, ds.Namespace, ds.Name)
	klog.Infof("Start rolling update for %s/%s", ds.Namespace, ds.Name)
	err = dsc.rollingUpdate(ds, nodeList, hash)
	if err != nil {
		return err
	}

	return nil
}

func (dsc *ReconcileDaemonSet) getDaemonSetsForPod(pod *corev1.Pod) []*apps.DaemonSet {
	sets, err := dsc.GetPodDaemonSets(pod)
	if err != nil {
		return nil
	}
	if len(sets) > 1 {
		// ControllerRef will ensure we don't do anything crazy, but more than one
		// item in this list nevertheless constitutes user error.
		utilruntime.HandleError(fmt.Errorf("user error! more than one daemon is selecting pods with labels: %+v", pod.Labels))
	}
	return sets
}

// Predicates checks if a DaemonSet's pod can run on a node.
func Predicates(pod *corev1.Pod, node *corev1.Node, taints []corev1.Taint) (fitsNodeName, fitsNodeAffinity, fitsTaints bool) {
	fitsNodeName = len(pod.Spec.NodeName) == 0 || pod.Spec.NodeName == node.Name
	// Ignore parsing errors for backwards compatibility.
	fitsNodeAffinity, _ = nodeaffinity.GetRequiredNodeAffinity(pod).Match(node)
	_, hasUntoleratedTaint := v1helper.FindMatchingUntoleratedTaint(taints, pod.Spec.Tolerations, func(t *corev1.Taint) bool {
		return t.Effect == corev1.TaintEffectNoExecute || t.Effect == corev1.TaintEffectNoSchedule
	})
	fitsTaints = !hasUntoleratedTaint
	return
}

func isControlledByDaemonSet(p *corev1.Pod, uuid types.UID) bool {
	for _, ref := range p.OwnerReferences {
		if ref.Controller != nil && *ref.Controller && ref.UID == uuid {
			return true
		}
	}
	return false
}

// NewPod creates a new pod
func NewPod(ds *apps.DaemonSet, nodeName string) *corev1.Pod {
	// firstly load the cache before lock
	if pod := loadNewPodForDS(ds); pod != nil {
		return pod
	}

	newPodForDSLock.Lock()
	defer newPodForDSLock.Unlock()

	// load the cache again after locked
	if pod := loadNewPodForDS(ds); pod != nil {
		return pod
	}

	newPod := &corev1.Pod{Spec: ds.Spec.Template.Spec, ObjectMeta: ds.Spec.Template.ObjectMeta}
	newPod.Namespace = ds.Namespace
	// no need to set nodeName
	// newPod.Spec.NodeName = nodeName

	// Added default tolerations for DaemonSet pods.
	daemonsetutil.AddOrUpdateDaemonPodTolerations(&newPod.Spec)

	newPodForDSCache.Store(ds.UID, &newPodForDS{generation: ds.Generation, pod: newPod})
	return newPod
}

// syncNodes deletes given pods and creates new daemon set pods on the given nodes
// returns slice with erros if any
func (dsc *ReconcileDaemonSet) syncNodes(ds *apps.DaemonSet, podsToDelete, nodesNeedingDaemonPods []string, hash string) error {
	klog.Infof("syncNodes() ")

	podsToDelete, err := dsc.syncWithPreDeleteHooks(ds, podsToDelete)
	if err != nil {
		return err
	}

	dsKey := keyFunc(ds)
	createDiff := len(nodesNeedingDaemonPods)
	deleteDiff := len(podsToDelete)

	// error channel to communicate back failures.  make the buffer big enough to avoid any blocking
	errCh := make(chan error, createDiff+deleteDiff)

	klog.Infof("Pods to delete for DaemonSet %s: %+v, deleting %d", ds.Name, podsToDelete, deleteDiff)
	deleteWait := sync.WaitGroup{}
	deleteWait.Add(deleteDiff)
	for i := 0; i < deleteDiff; i++ {
		go func(ix int) {
			defer deleteWait.Done()
			pod := podsToDelete[ix]
			klog.V(2).Infof("Try to delete pod %v", pod)
			if err := dsc.podControl.DeletePod(ds.Namespace, podsToDelete[ix], ds); err != nil {
				dsc.expectations.DeletionObserved(dsKey)
				if !errors.IsNotFound(err) {
					klog.V(2).Infof("Failed deletion, decremented expectations for set %q/%q", ds.Namespace, ds.Name)
					errCh <- err
					utilruntime.HandleError(err)
				}
			} else {
				klog.V(2).Infof("pod %v is deleted", pod)
			}
		}(i)
	}
	deleteWait.Wait()
	// collect errors if any for proper reporting/retry logic in the controller
	var errors []error
	close(errCh)
	for err := range errCh {
		errors = append(errors, err)
	}
	return utilerrors.NewAggregate(errors)
}

func (dsc *ReconcileDaemonSet) syncWithPreDeleteHooks(ds *apps.DaemonSet, podsToDelete []string) (podsCanDelete []string, err error) {
	// get hooks from deamonset, use default hook
	hooks := map[string]string{
		appspub.DaemonSetPrecheckHookKey: "",
		//"DeviceLock": "",
	}
	for _, podName := range podsToDelete {
		pod, err := dsc.podLister.Pods(ds.Namespace).Get(podName)
		if errors.IsNotFound(err) {
			continue
		} else if err != nil {
			return nil, err
		}

		verifiedValue := 0
		for hk, _ := range hooks {
			klog.V(3).Infof("DaemonSet %s/%s check hook %v for pod %v", ds.Namespace, ds.Name, hk, podName)
			precheckValue, found := pod.Annotations[hk]
			if !found {
				klog.V(3).Infof("DaemonSet %s/%s hook %v is not found for pod %v", ds.Namespace, ds.Name, hk, podName)
				// clean post check
				dsc.UpdatePodAnnotation(pod, string(appspub.DaemonSetPostcheckHookKey), "")
				dsc.UpdatePodAnnotation(pod, string(appspub.DaemonSetPrecheckHookKey), string(appspub.DaemonSetHookStatePending))
				dsc.eventRecorder.Eventf(ds, corev1.EventTypeNormal, "PodPreCheckPending", fmt.Sprintf("The pod %v update is pending for precheck now.", podName))
				continue
			} else {
				if strings.EqualFold(precheckValue, string(appspub.DaemonSetHookStateCompleted)) {
					klog.V(3).Infof("DaemonSet %s/%s hook %v is done for pod %v", ds.Namespace, ds.Name, hk, podName)
					dsc.eventRecorder.Eventf(ds, corev1.EventTypeNormal, "PodPreCheckSuccess", fmt.Sprintf("The pod %v precheck was completed.", podName))
					verifiedValue++
				} else if !strings.EqualFold(precheckValue, string(appspub.DaemonSetHookStatePending)) {
					dsc.UpdatePodAnnotation(pod, string(appspub.DaemonSetPostcheckHookKey), "")
					dsc.UpdatePodAnnotation(pod, string(appspub.DaemonSetPrecheckHookKey), string(appspub.DaemonSetHookStatePending))
					klog.V(3).Infof("DaemonSet %s/%s hook %v is not done for pod %v, will pending the delete", ds.Namespace, ds.Name, hk, podName)
					dsc.eventRecorder.Eventf(ds, corev1.EventTypeNormal, "PodPreCheckPending", fmt.Sprintf("The pod %v update is pending for precheck now.", podName))
				}
			}
		}

		if verifiedValue >= len(hooks) {
			klog.V(3).Infof("DaemonSet %s/%s all hook are done for pod %v, it can proceed to delete.", ds.Namespace, ds.Name, podName)
			podsCanDelete = append(podsCanDelete, podName)
		}
	}
	return
}

// getNodesToDaemonPods returns a map from nodes to daemon pods (corresponding to ds) created for the nodes.
// This also reconciles ControllerRef by adopting/orphaning.
// Note that returned Pods are pointers to objects in the cache.
// If you want to modify one, you need to deep-copy it first.
func (dsc *ReconcileDaemonSet) getNodesToDaemonPods(ds *apps.DaemonSet) (map[string][]*corev1.Pod, error) {
	claimedPods, err := dsc.getDaemonPods(ds)
	if err != nil {
		return nil, err
	}
	// Group Pods by Node name.
	nodeToDaemonPods := make(map[string][]*corev1.Pod)
	for _, pod := range claimedPods {
		nodeName, err := util.GetTargetNodeName(pod)
		if err != nil {
			klog.Warningf("Failed to get target node name of Pod %v/%v in DaemonSet %v/%v", pod.Namespace, pod.Name, ds.Namespace, ds.Name)
			continue
		}
		nodeToDaemonPods[nodeName] = append(nodeToDaemonPods[nodeName], pod)
	}

	return nodeToDaemonPods, nil
}

func failedPodsBackoffKey(ds *apps.DaemonSet, nodeName string) string {
	return fmt.Sprintf("%s/%d/%s", ds.UID, ds.Status.ObservedGeneration, nodeName)
}

type podByCreationTimestampAndPhase []*corev1.Pod

func (o podByCreationTimestampAndPhase) Len() int      { return len(o) }
func (o podByCreationTimestampAndPhase) Swap(i, j int) { o[i], o[j] = o[j], o[i] }

func (o podByCreationTimestampAndPhase) Less(i, j int) bool {
	// Scheduled Pod first
	if len(o[i].Spec.NodeName) != 0 && len(o[j].Spec.NodeName) == 0 {
		return true
	}

	if len(o[i].Spec.NodeName) == 0 && len(o[j].Spec.NodeName) != 0 {
		return false
	}

	if o[i].CreationTimestamp.Equal(&o[j].CreationTimestamp) {
		return o[i].Name < o[j].Name
	}
	return o[i].CreationTimestamp.Before(&o[j].CreationTimestamp)
}
