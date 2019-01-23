/*
Copyright The Kubernetes Authors.

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

// Code generated by informer-gen. DO NOT EDIT.

package v1beta1

import (
	internalinterfaces "github.com/rook/rook/pkg/client/informers/externalversions/internalinterfaces"
)

// Interface provides access to all the informers in this group version.
type Interface interface {
	// CSIDrivers returns a CSIDriverInformer.
	CSIDrivers() CSIDriverInformer
	// Clusters returns a ClusterInformer.
	Clusters() ClusterInformer
	// Filesystems returns a FilesystemInformer.
	Filesystems() FilesystemInformer
	// ObjectStores returns a ObjectStoreInformer.
	ObjectStores() ObjectStoreInformer
	// ObjectStoreUsers returns a ObjectStoreUserInformer.
	ObjectStoreUsers() ObjectStoreUserInformer
	// Pools returns a PoolInformer.
	Pools() PoolInformer
}

type version struct {
	factory          internalinterfaces.SharedInformerFactory
	namespace        string
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// New returns a new Interface.
func New(f internalinterfaces.SharedInformerFactory, namespace string, tweakListOptions internalinterfaces.TweakListOptionsFunc) Interface {
	return &version{factory: f, namespace: namespace, tweakListOptions: tweakListOptions}
}

// CSIDrivers returns a CSIDriverInformer.
func (v *version) CSIDrivers() CSIDriverInformer {
	return &cSIDriverInformer{factory: v.factory, namespace: v.namespace, tweakListOptions: v.tweakListOptions}
}

// Clusters returns a ClusterInformer.
func (v *version) Clusters() ClusterInformer {
	return &clusterInformer{factory: v.factory, namespace: v.namespace, tweakListOptions: v.tweakListOptions}
}

// Filesystems returns a FilesystemInformer.
func (v *version) Filesystems() FilesystemInformer {
	return &filesystemInformer{factory: v.factory, namespace: v.namespace, tweakListOptions: v.tweakListOptions}
}

// ObjectStores returns a ObjectStoreInformer.
func (v *version) ObjectStores() ObjectStoreInformer {
	return &objectStoreInformer{factory: v.factory, namespace: v.namespace, tweakListOptions: v.tweakListOptions}
}

// ObjectStoreUsers returns a ObjectStoreUserInformer.
func (v *version) ObjectStoreUsers() ObjectStoreUserInformer {
	return &objectStoreUserInformer{factory: v.factory, namespace: v.namespace, tweakListOptions: v.tweakListOptions}
}

// Pools returns a PoolInformer.
func (v *version) Pools() PoolInformer {
	return &poolInformer{factory: v.factory, namespace: v.namespace, tweakListOptions: v.tweakListOptions}
}
