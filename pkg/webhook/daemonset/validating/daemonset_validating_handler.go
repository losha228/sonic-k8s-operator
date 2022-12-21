/*
Copyright 2020 The Sonic_k8s Authors.

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

package validating

import (
	"context"
	"net/http"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	apps "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// ValidateDaemonSetName can be used to check whether the given daemon set name is valid.
// Prefix indicates this name will be used as part of generation, in which case
// trailing dashes are allowed.
var ValidateDaemonSetNamespace = "sonic"

// DaemonSetCreateUpdateHandler handles DaemonSet
type DaemonSetCreateUpdateHandler struct {
	// Decoder decodes objects
	Decoder *admission.Decoder
}

func (h *DaemonSetCreateUpdateHandler) validatingDaemonSet(ctx context.Context, ds *apps.DaemonSet) (bool, string, error) {
	// we only validate ds in sonic namespace
	klog.Infof("Validate ds %s/%s", ds.Namespace, ds.Name)
	if strings.EqualFold(ds.Namespace, ValidateDaemonSetNamespace) {
		fldPath := field.NewPath("spec")
		allErrs := validateDaemonSetUpdateStrategy(ds, &ds.Spec.UpdateStrategy, fldPath.Child("updateStrategy"))
		if len(allErrs) != 0 {
			return false, "", allErrs.ToAggregate()
		}
	} else {
		klog.Infof("Ignore ds %s/%s as it is not in %v namespace.", ds.Namespace, ds.Name, ValidateDaemonSetNamespace)
	}
	return true, "Allowed to be admitted", nil
}

func validateDaemonSetUpdateStrategy(ds *apps.DaemonSet, strategy *apps.DaemonSetUpdateStrategy, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	klog.Infof("Validate ds %s/%s update strategy %v", ds.Namespace, ds.Name, strategy.Type)
	if strategy.Type != apps.OnDeleteDaemonSetStrategyType {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("type"), strategy.Type, "Only OnDelete is supported!"))
		return allErrs
	}

	return allErrs
}

var _ admission.Handler = &DaemonSetCreateUpdateHandler{}

// Handle handles admission requests.
func (h *DaemonSetCreateUpdateHandler) Handle(ctx context.Context, req admission.Request) admission.Response {

	if req.AdmissionRequest.Operation != admissionv1.Create && req.AdmissionRequest.Operation != admissionv1.Update {
		return admission.ValidationResponse(true, "")
	}

	obj := &apps.DaemonSet{}

	err := h.Decoder.Decode(req, obj)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	allowed, reason, err := h.validatingDaemonSet(ctx, obj)
	if err != nil {
		klog.Warningf("ds %s/%s action %v fail:%s", obj.Namespace, obj.Name, req.AdmissionRequest.Operation, err.Error())
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.ValidationResponse(allowed, reason)
}

var _ admission.DecoderInjector = &DaemonSetCreateUpdateHandler{}

func (h *DaemonSetCreateUpdateHandler) InjectDecoder(d *admission.Decoder) error {
	h.Decoder = d
	return nil
}
