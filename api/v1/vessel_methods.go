package v1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// SetStatus returns another VesselNewStatus for convience of chaining, only a single optional override is accepted
func (s *VesselNewStatus) SetStatus(ctx context.Context, override ...*VesselNewStatusData) *VesselNewStatus {
	if len(override) > 0 {
		if override[0].Reason != "" {
			s.Data.Reason = override[0].Reason
		}
		if override[0].Message != "" {
			s.Data.Message = override[0].Message
		}
		if override[0].Condition != "" {
			s.Data.Condition = override[0].Condition
		}
		if override[0].Status != "" {
			s.Data.Status = override[0].Status
		}
	}
	condition := string(s.Data.Condition)
	reason := string(s.Data.Reason)
	status := s.Data.Status
	meta.SetStatusCondition(&s.Vessel.Status.Conditions, metav1.Condition{Type: condition, Status: status, Reason: reason, Message: s.Data.Message, ObservedGeneration: s.Vessel.Generation})
	logf.FromContext(ctx).Info(fmt.Sprintf(`updated condition status %s to %s. Reason: %q Message: %q`, condition, status, s.Data.Reason, s.Data.Message))
	return s
}

func (v *Vessel) SetStatusFullyAvailable(ctx context.Context, statusInfo *VesselNewStatusInfo) {
	v.SetStatusAvailable(ctx, metav1.ConditionTrue, statusInfo)
	v.SetStatusDegraded(ctx, metav1.ConditionFalse, statusInfo)
	v.SetStatusProgressing(ctx, metav1.ConditionFalse, statusInfo)
}

func (v *Vessel) SetStatusFullyProgressing(ctx context.Context, statusInfo *VesselNewStatusInfo) {
	v.SetStatusAvailable(ctx, metav1.ConditionUnknown, statusInfo)
	v.SetStatusDegraded(ctx, metav1.ConditionUnknown, statusInfo)
	v.SetStatusProgressing(ctx, metav1.ConditionTrue, statusInfo)
}

func (v *Vessel) SetStatusFullyDegraded(ctx context.Context, statusInfo *VesselNewStatusInfo) {
	v.SetStatusAvailable(ctx, metav1.ConditionFalse, statusInfo)
	v.SetStatusDegraded(ctx, metav1.ConditionTrue, statusInfo)
	v.SetStatusProgressing(ctx, metav1.ConditionFalse, statusInfo)
}

func (v *Vessel) SetStatusUnknown(ctx context.Context, statusInfo *VesselNewStatusInfo) {
	v.SetStatusAvailable(ctx, metav1.ConditionUnknown, statusInfo)
	v.SetStatusDegraded(ctx, metav1.ConditionUnknown, statusInfo)
	v.SetStatusProgressing(ctx, metav1.ConditionUnknown, statusInfo)
}

func (v *Vessel) SetStatusAvailable(ctx context.Context, condition metav1.ConditionStatus, statusInfo *VesselNewStatusInfo) {
	newStatus := &VesselNewStatus{
		Vessel: v,
		Data: &VesselNewStatusData{
			Condition:           ConditionTypeAvailable,
			Status:              condition,
			VesselNewStatusInfo: statusInfo,
		},
	}
	newStatus.SetStatus(ctx)
}

func (v *Vessel) SetStatusProgressing(ctx context.Context, condition metav1.ConditionStatus, statusInfo *VesselNewStatusInfo) {
	newStatus := &VesselNewStatus{
		Vessel: v,
		Data: &VesselNewStatusData{
			Condition:           ConditionTypeProgressing,
			Status:              condition,
			VesselNewStatusInfo: statusInfo,
		},
	}
	newStatus.SetStatus(ctx)
}

func (v *Vessel) SetStatusDegraded(ctx context.Context, condition metav1.ConditionStatus, statusInfo *VesselNewStatusInfo) {
	newStatus := &VesselNewStatus{
		Vessel: v,
		Data: &VesselNewStatusData{
			Condition:           ConditionTypeDegraded,
			Status:              condition,
			VesselNewStatusInfo: statusInfo,
		},
	}
	newStatus.SetStatus(ctx)
}

// GetOwnedGVKList get owned GVK lists
func (v *Vessel) GetOwnedGVKList() []schema.GroupVersionKind {
	var list []schema.GroupVersionKind
	if v.Spec.Servers != nil {
		list = []schema.GroupVersionKind{
			{
				Group:   "apps",
				Version: "v1",
				Kind:    "DeploymentList",
			},
			{
				Group:   "",
				Version: "v1",
				Kind:    "ServiceList",
			},
			{
				Group:   "gateway.networking.k8s.io",
				Version: "v1",
				Kind:    "HTTPRoute",
			},
			{
				Group:   "external-secrets.io",
				Version: "v1",
				Kind:    "ExternalSecretList",
			},
			{
				Group:   "db.atlasgo.io",
				Version: "v1alpha1",
				Kind:    "AtlasSchemaList",
			},
			{
				Group:   "batch",
				Version: "v1",
				Kind:    "JobList",
			},
		}
	}
	return list
}

// resources
