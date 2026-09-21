/*
Copyright 2026.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ExternalSecretData type to work with secret data on ESO
type ExternalSecretData struct {
	// +required
	SecretKey string `json:"secretKey"`
	// +required
	Key string `json:"key"`
	// +required
	Property string `json:"property"`
}

// ExternalSecret type to work with ESO
type ExternalSecret struct {
	// +required
	Name string `json:"name"`
	// +required
	Data []ExternalSecretData `json:"data"`

	// +optional
	// +kubebuilder:validation:Enum=CreatedOnce;Periodic;OnChange
	RefreshPolicy *esv1.ExternalSecretRefreshPolicy `json:"refreshPolicy,omitempty"`
	// +optional
	RefreshInterval *metav1.Duration `json:"refreshInterval,omitempty"`
	// +optional
	SecretStoreRef *esv1.SecretStoreRef `json:"secretStoreRef,omitempty"`
}

// Database will apply configmap with atlasschema to use
type Database struct {
	// +required
	Name string `json:"name"`
	// +required
	Schema string `json:"schema"`
}

// VesselServer will apply
// deployment
// service
// httproute
type VesselServer struct {
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +required
	Image string `json:"image"`
	// +required
	Port int32 `json:"port"`

	// +optional
	ExternalSecrets []ExternalSecret `json:"externalSecrets,omitempty"`
	// +optional
	Envs map[string]string `json:"envs,omitempty"`
	// +optional
	EnvSecretRefs []string `json:"envSecretRefs,omitempty"`
	// +optional
	Databases []Database `json:"databases,omitempty"`
}

// VesselSpec defines the desired state of Vessel
type VesselSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// +kubebuilder:default=`default`
	Profile string `json:"profile"`
	// +listType=map
	// +listMapKey=name
	// +optional
	Servers []*VesselServer `json:"servers,omitempty"`
}

// VesselStatus defines the observed state of Vessel.
type VesselStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the Vessel resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Vessel is the Schema for the vessels API
type Vessel struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Vessel
	// +required
	Spec VesselSpec `json:"spec"`

	// status defines the observed state of Vessel
	// +optional
	Status VesselStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// VesselList contains a list of Vessel
type VesselList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Vessel `json:"items"`
}

type (
	ConditionType string
	ReasonType    string
)

const (
	ConditionTypeAvailable   ConditionType = "Available"
	ConditionTypeProgressing ConditionType = "Progressing"
	ConditionTypeDegraded    ConditionType = "Degraded"
)

const (
	ReasonProfileMissing      ReasonType = "ProfileMissing"
	ReasonFailed              ReasonType = "ReconcileFailed"
	ReasonSucceeded           ReasonType = "ReconcileSucceeded"
	ReasonReconcileInProgress ReasonType = "ReconcileInProgress"
	ReasonApplyInProgress     ReasonType = "ApplyInProgress"
	ReasonApplyFailed         ReasonType = "ApplyFailed"
	ReasonApplySucceeded      ReasonType = "ApplySucceeded"
	ReasonResourcesInProgress ReasonType = "ResourcesInProgress"
	ReasonResourcesUnhealthy  ReasonType = "ResourcesUnhealthy"
)

type VesselNewStatus struct {
	Data   *VesselNewStatusData
	Vessel *Vessel
}

type VesselNewStatusData struct {
	*VesselNewStatusInfo
	Condition ConditionType
	Status    metav1.ConditionStatus
}

type VesselNewStatusInfo struct {
	Reason  ReasonType
	Message string
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Vessel{}, &VesselList{})
		return nil
	})
}
