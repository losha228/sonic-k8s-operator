/*
Copyright 2022 The Sonic_k8s Authors.

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

package hookchecker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appspub "github.com/sonic-net/sonic-k8s-operator/apis/apps/pub"
	ctrutil "github.com/sonic-net/sonic-k8s-operator/pkg/util"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	//"sigs.k8s.io/controller-runtime/pkg/client"
	"github.com/sonic-net/sonic-k8s-operator/pkg/client"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var (
	concurrentReconciles = 3
	controllerKind       = apps.SchemeGroupVersion.WithKind("DaemonSet")
)

func Add(mgr manager.Manager) error {
	r, err := newReconciler(mgr)
	if err != nil {
		return err
	}
	return add(mgr, r)
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) (*ReconcileHookChecker, error) {
	genericClient := client.GetGenericClientWithName("hook-controller")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: genericClient.KubeClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "sonic-hook-checker-controller"})
	cacher := mgr.GetCache()
	dsInformer, err := cacher.GetInformerForKind(context.TODO(), apps.SchemeGroupVersion.WithKind("DaemonSet"))
	if err != nil {
		return nil, err
	}
	dsLister := appslisters.NewDaemonSetLister(dsInformer.(cache.SharedIndexInformer).GetIndexer())
	return &ReconcileHookChecker{
		/// Client:     utilclient.NewClientFromManager(mgr, "pod-hook-controller"),
		dsLister:      dsLister,
		kubeClient:    genericClient.KubeClient,
		eventRecorder: recorder,
	}, nil
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r *ReconcileHookChecker) error {
	// Create a new controller
	c, err := controller.New("pod-hook-controller", mgr, controller.Options{Reconciler: r, MaxConcurrentReconciles: concurrentReconciles})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &v1.Pod{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pod := e.Object.(*v1.Pod)
			return r.checkCondition(pod)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			pod := e.ObjectNew.(*v1.Pod)
			return r.checkCondition(pod)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileHookChecker{}

// ReconcilePodReadiness reconciles a Pod object
type ReconcileHookChecker struct {
	// client.Client
	kubeClient clientset.Interface
	// dsLister can list/get daemonsets from the shared informer's store
	dsLister      appslisters.DaemonSetLister
	eventRecorder record.EventRecorder
}

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;update;patch

func (r *ReconcileHookChecker) Reconcile(_ context.Context, request reconcile.Request) (res reconcile.Result, err error) {
	start := time.Now()
	klog.V(3).Infof("Starting to process Pod %v", request.NamespacedName)
	defer func() {
		if err != nil {
			klog.Warningf("Failed to process Pod %v, elapsedTime %v, error: %v", request.NamespacedName, time.Since(start), err)
		} else {
			klog.Infof("Finish to process Pod %v, elapsedTime %v", request.NamespacedName, time.Since(start))
		}
	}()

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// err = r.Get(context.TODO(), request.NamespacedName, pod)
		pod, err := r.kubeClient.CoreV1().Pods(request.Namespace).Get(context.TODO(), request.Name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				// Object not found, return.  Created objects are automatically garbage collected.
				// For additional cleanup logic use finalizers.
				return nil
			}
			// Error reading the object - requeue the request.
			return err
		}
		if pod.DeletionTimestamp != nil {
			return nil
		}

		// check precheck hook
		if precheck, prechckFound := pod.Annotations[string(appspub.DaemonSetPrecheckHookKey)]; prechckFound {
			if strings.EqualFold(precheck, string(appspub.DaemonSetHookStatePending)) {
				precheckErr := r.doCheck(pod, string(appspub.DaemonSetPrecheckHookKey))
				if precheckErr != nil {
					return precheckErr
				}
			}
		}

		// check postcheck hook
		if postcheck, postchckFound := pod.Annotations[string(appspub.DaemonSetPostcheckHookKey)]; postchckFound {
			if strings.EqualFold(postcheck, string(appspub.DaemonSetHookStatePending)) {
				postcheckErr := r.doCheck(pod, string(appspub.DaemonSetPostcheckHookKey))
				if postcheckErr != nil {
					return postcheckErr
				}
			}
		}

		return nil
	})

	if err != nil {
		return reconcile.Result{RequeueAfter: time.Duration(30) * time.Minute}, err
	}
	return reconcile.Result{}, err
}

func (r *ReconcileHookChecker) doCheck(pod *v1.Pod, checkType string) error {

	// sleep 10 second and make check completed
	klog.V(4).Infof("%s for pod %s/%s is started", checkType, pod.Namespace, pod.Name)
	time.Sleep(time.Duration(10) * time.Second)
	msg := fmt.Sprintf("%v for %v", checkType, pod.Spec.Containers[0].Image)
	newCheckDetails := &appspub.DaemonSetHookDetails{Type: checkType, Message: msg, Status: string(appspub.DaemonSetHookStateCompleted)}
	newCheckDetails.LastProbeTime = metav1.Now()
	detailKey := ""
	if checkType == string(appspub.DaemonSetPrecheckHookKey) {
		detailKey = string(appspub.DaemonSetPrecheckHookProbeDetailsKey)
	} else if checkType == string(appspub.DaemonSetPostcheckHookKey) {
		detailKey = string(appspub.DaemonSetPostcheckHookProbeDetailsKey)
	}

	r.updatePodAnnotation(pod, checkType, string(appspub.DaemonSetHookStateCompleted), detailKey, newCheckDetails)
	ds, err := r.getPodDaemonSets(pod)
	if err == nil {
		r.eventRecorder.Eventf(ds, v1.EventTypeNormal, fmt.Sprintf("Pod%sSuccess", checkType), fmt.Sprintf("The %v for pod %v was completed.", checkType, pod.Name))
	}

	klog.V(4).Infof("%s for pod %s/%s is completed", checkType, pod.Namespace, pod.Name)
	return nil
}

func (r *ReconcileHookChecker) checkCondition(pod *v1.Pod) bool {
	// only care the pod in ds
	if controllerRef := metav1.GetControllerOf(pod); controllerRef != nil {
		if controllerRef.Kind != controllerKind.Kind || controllerRef.APIVersion != controllerKind.GroupVersion().String() {
			return false
		}
	}

	if _, hookCheckerEnabled := pod.Annotations[string(appspub.DaemonSetHookCheckerEnabledKey)]; !hookCheckerEnabled {
		klog.V(4).Infof("No %s for pod %s/%s, skip", string(appspub.DaemonSetHookCheckerEnabledKey), pod.Namespace, pod.Name)
		return false
	}

	precheck, prechckFound := pod.Annotations[string(appspub.DaemonSetPrecheckHookKey)]
	postcheck, postchckFound := pod.Annotations[string(appspub.DaemonSetPostcheckHookKey)]
	if !prechckFound && !postchckFound {
		return false
	}

	if !strings.EqualFold(precheck, string(appspub.DaemonSetHookStatePending)) && !strings.EqualFold(postcheck, string(appspub.DaemonSetHookStatePending)) {
		klog.V(4).Infof("No pending hook for pod %s/%s, skip", pod.Namespace, pod.Name)
		return false
	}
	return true
}

func (r *ReconcileHookChecker) updatePodAnnotation(pod *v1.Pod, key, value, detailsKey string, details *appspub.DaemonSetHookDetails) (updated bool, err error) {
	if pod == nil {
		return false, nil
	}

	pod = pod.DeepCopy()

	dataStr, err := json.Marshal(details)
	if err != nil {
		return false, err
	}

	// escape the ""
	detailStr, err := json.Marshal(string(dataStr))
	if err != nil {
		return false, err
	}

	body := fmt.Sprintf(
		`{"metadata":{"annotations":{"%s":"%s", "%s": %s }}}`,
		key,
		value,
		detailsKey,
		string(detailStr))

	r.kubeClient.CoreV1().Pods(pod.Namespace).Patch(context.TODO(), pod.Name, types.StrategicMergePatchType, []byte(body), metav1.PatchOptions{})

	return true, err
}

func (r *ReconcileHookChecker) getPodDaemonSets(pod *v1.Pod) (*apps.DaemonSet, error) {
	if len(pod.Labels) == 0 {
		return nil, fmt.Errorf("no daemon sets found for pod %v because it has no labels", pod.Name)
	}

	dsList, err := r.dsLister.DaemonSets(pod.Namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	var selector labels.Selector
	var daemonSets []*apps.DaemonSet
	for _, ds := range dsList {
		selector, err = ctrutil.ValidatedLabelSelectorAsSelector(ds.Spec.Selector)
		if err != nil {
			// this should not happen if the DaemonSet passed validation
			return nil, err
		}

		// If a daemonSet with a nil or empty selector creeps in, it should match nothing, not everything.
		if selector.Empty() || !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		daemonSets = append(daemonSets, ds)
	}

	if len(daemonSets) == 0 {
		return nil, fmt.Errorf("could not find daemon set for pod %s in namespace %s with labels: %v", pod.Name, pod.Namespace, pod.Labels)
	}

	return daemonSets[0], nil
}
