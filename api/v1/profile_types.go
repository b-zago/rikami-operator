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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	esv1 "github.com/external-secrets/external-secrets/apis/externalsecrets/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ExternalSecretsConfig determines default configuration for ExternalSecrets resources
type ExternalSecretsConfig struct {
	// +required
	// +kubebuilder:validation:Enum=CreatedOnce;Periodic;OnChange
	RefreshPolicy *esv1.ExternalSecretRefreshPolicy `json:"refreshPolicy,omitempty"`
	// +required
	RefreshInterval *metav1.Duration `json:"refreshInterval,omitempty"`
	// +required
	SecretStoreRef *esv1.SecretStoreRef `json:"secretStoreRef,omitempty"`
}

// DatabaseAppConfig configures creation of externalsecret that would be injected as env into pods for app access to db
type DatabaseAppConfig struct {
	// +required
	UsernameKey string `json:"usernameKey"`
	// +required
	PasswordKey string `json:"passwordKey"`
	// +required
	HostKey string `json:"hostKey"`
	// +required
	SecretAppPath string `json:"secretAppPath"`
}

// DatabaseMigratorConfig configures creation of externalsecret that would be used by atlasschema to get the pg url
type DatabaseMigratorConfig struct {
	// +required
	SecretMigratorPath string `json:"secretMigratorPath"`
	// +required
	SecretKey string `json:"secretKey"`
}

// DatabaseConfig determines default parameters to create external secret containing pg url login for atlas migration/schema role
type DatabaseConfig struct {
	// +required
	DatabaseAppConfig DatabaseAppConfig `json:"appConfig"`
	// +required
	DatabaseMigratorConfig DatabaseMigratorConfig `json:"migratorConfig"`
	// +kubebuilder:default=db_
	EnvKeysPrefix string `json:"envKeysPrefix"`
}

// ProfileSpec defines the desired state of Profile
type ProfileSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// +required
	// +kubebuilder:validation:MinLength=1
	Gateway string `json:"gateway"`
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +required
	PullPolicy corev1.PullPolicy `json:"pullPolicy"`
	// +optional
	// +kubebuilder:validation:MinLength=1
	PullSecret *string `json:"pullSecret,omitempty"`
	// +kubebuilder:validation:MinLength=4
	// +kubebuilder:validation:Pattern=`[A-Za-z0-9]+\.[A-Za-z]+`
	// +required
	Domain string `json:"domain"`
	// +kubebuilder:default=false
	NamespacedSubdomain bool `json:"namespacedSubdomain"`
	// +required
	ExternalSecretsConfig ExternalSecretsConfig `json:"externalSecretsConfig"`
	// +required
	DatabaseConfig DatabaseConfig `json:"databaseConfig"`
}

// ProfileStatus defines the observed state of Profile.
type ProfileStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the Profile resource.
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

// Profile is the Schema for the profiles API
type Profile struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Profile
	// +required
	Spec ProfileSpec `json:"spec"`

	// status defines the observed state of Profile
	// +optional
	Status ProfileStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProfileList contains a list of Profile
type ProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Profile `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Profile{}, &ProfileList{})
		return nil
	})
}
