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

package pub

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	DaemonSetPrecheckHookKey              = "hook.daemonset.sonic/precheck"
	DaemonSetPrecheckHookCheckDetailsKey  = "hook.daemonset.sonic/precheck-check-details"
	DaemonSetPrecheckHookProbeDetailsKey  = "hook.daemonset.sonic/precheck-probe-details"
	DaemonSetPostcheckHookKey             = "hook.daemonset.sonic/postcheck"
	DaemonSetEventPublishedHookKey        = "hook.daemonset.sonic/event-published"
	DaemonSetPostcheckHookCheckDetailsKey = "hook.daemonset.sonic/postcheck-check-details"
	DaemonSetPostcheckHookProbeDetailsKey = "hook.daemonset.sonic/postcheck-probe-details"
	DaemonSetHookCheckerEnabledKey        = "hook.daemonset.sonic/hook-checker-enabled"

	DaemonSetHookStatePending   DaemonsetHookStateType = "pending"
	DaemonSetHookStateCompleted DaemonsetHookStateType = "completed"
	DaemonSetHookStateFailed    DaemonsetHookStateType = "failed"

	DaemonSetDeploymentPausedKey = "deployment.daemonset.sonic/paused"
)

type DaemonsetHookStateType string

type DaemonSetHookDetails struct {
	// Type is the type of the condition.
	// More info: https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle#pod-conditions
	Type string `json:"type"`

	// precheck probe result, true or false.
	Result bool `json:"result,omitempty"`

	// Last time we probed the condition.
	// +optional
	LastProbeTime metav1.Time `json:"lastProbeTime,omitempty"`

	// hook status.
	// +optional
	Status string `json:"status,omitempty"`

	// Human-readable message indicating details about last transition.
	// +optional
	Message string `json:"message,omitempty"`
}
