/*
Copyright 2016 The Rook Authors. All rights reserved.

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

// Package osd for the Ceph OSDs.
package osd

import (
	"strconv"
	"testing"

	rookalpha "github.com/rook/rook/pkg/apis/rook.io/v1alpha1"
	"github.com/rook/rook/pkg/clusterd"
	exectest "github.com/rook/rook/pkg/util/exec/test"
	"github.com/stretchr/testify/assert"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubernetes/pkg/kubelet/apis"
)

func TestPodContainer(t *testing.T) {
	cluster := &Cluster{Namespace: "myosd", Version: "23"}
	config := rookalpha.Config{}
	c := cluster.podTemplateSpec([]rookalpha.Device{}, rookalpha.Selection{}, v1.ResourceRequirements{}, config, false /* prepareOnly */, v1.RestartPolicyAlways)
	assert.NotNil(t, c)
	assert.Equal(t, 1, len(c.Spec.Containers))
	container := c.Spec.Containers[0]
	assert.Equal(t, "osd", container.Args[0])
}

func TestDaemonset(t *testing.T) {
	testPodDevices(t, "", "sda", true)
	testPodDevices(t, "/var/lib/mydatadir", "sdb", false)
	testPodDevices(t, "", "", true)
	testPodDevices(t, "", "", false)
}

func testPodDevices(t *testing.T, dataDir, deviceName string, allDevices bool) {
	storageSpec := rookalpha.StorageSpec{
		Selection: rookalpha.Selection{UseAllDevices: &allDevices, DeviceFilter: deviceName},
		Nodes:     []rookalpha.Node{{Name: "node1"}},
	}
	devices := []rookalpha.Device{
		{Name: deviceName},
	}

	clientset := fake.NewSimpleClientset()
	c := New(&clusterd.Context{Clientset: clientset, ConfigDir: "/var/lib/rook", Executor: &exectest.MockExecutor{}}, "ns", "rook/rook:myversion",
		storageSpec, dataDir, rookalpha.Placement{}, false, v1.ResourceRequirements{}, metav1.OwnerReference{})

	devMountNeeded := deviceName != "" || allDevices

	n := c.Storage.ResolveNode(storageSpec.Nodes[0].Name)
	if len(devices) == 0 && len(dataDir) == 0 {
		return
	}
	osd := OSDInfo{
		ID: 0,
	}

	deployment := c.makeOSDDeployment(n.Name, devices, n.Selection, v1.ResourceRequirements{}, osd)
	assert.NotNil(t, deployment)
	assert.Equal(t, "rook-ceph-osd-id-0", deployment.Name)
	assert.Equal(t, c.Namespace, deployment.Namespace)
	assert.Equal(t, int32(1), *(deployment.Spec.Replicas))
	assert.Equal(t, "node1", deployment.Spec.Template.Spec.NodeSelector[apis.LabelHostname])
	assert.Equal(t, v1.RestartPolicyAlways, deployment.Spec.Template.Spec.RestartPolicy)
	if devMountNeeded && len(dataDir) > 0 {
		assert.Equal(t, 3, len(deployment.Spec.Template.Spec.Volumes))
	}
	if devMountNeeded && len(dataDir) == 0 {
		assert.Equal(t, 2, len(deployment.Spec.Template.Spec.Volumes))
	}
	if !devMountNeeded && len(dataDir) > 0 {
		assert.Equal(t, 2, len(deployment.Spec.Template.Spec.Volumes))
	}

	//assert.Equal(t, "rook-data", deployment.Spec.Template.Spec.Volumes[0].Name)
	assert.Equal(t, "rook-config-override", deployment.Spec.Template.Spec.Volumes[0].Name)

	assert.Equal(t, appName, deployment.Spec.Template.ObjectMeta.Name)
	assert.Equal(t, appName, deployment.Spec.Template.ObjectMeta.Labels["app"])
	assert.Equal(t, c.Namespace, deployment.Spec.Template.ObjectMeta.Labels["rook_cluster"])
	assert.Equal(t, 0, len(deployment.Spec.Template.ObjectMeta.Annotations))

	cont := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "rook/rook:myversion", cont.Image)
	if devMountNeeded {
		assert.Equal(t, 3, len(cont.VolumeMounts))
	} else {
		assert.Equal(t, 3, len(cont.VolumeMounts))
	}
	assert.Equal(t, "sh", cont.Command[0])
}

func verifyEnvVar(t *testing.T, envVars []v1.EnvVar, expectedName, expectedValue string, expectedFound bool) {
	found := false
	for _, envVar := range envVars {
		if envVar.Name == expectedName {
			assert.Equal(t, expectedValue, envVar.Value)
			found = true
			break
		}
	}

	assert.Equal(t, expectedFound, found)
}

func TestStorageSpecDevicesAndDirectories(t *testing.T) {
	storageSpec := rookalpha.StorageSpec{
		Config: rookalpha.Config{},
		Selection: rookalpha.Selection{
			Directories: []rookalpha.Directory{{Path: "/rook/dir2"}},
		},
		Nodes: []rookalpha.Node{
			{
				Name:    "node1",
				Devices: []rookalpha.Device{{Name: "sda"}},
				Selection: rookalpha.Selection{
					Directories: []rookalpha.Directory{{Path: "/rook/dir1"}},
				},
			},
		},
	}

	clientset := fake.NewSimpleClientset()
	c := New(&clusterd.Context{Clientset: clientset, ConfigDir: "/var/lib/rook", Executor: &exectest.MockExecutor{}}, "ns", "rook/rook:myversion",
		storageSpec, "", rookalpha.Placement{}, false, v1.ResourceRequirements{}, metav1.OwnerReference{})

	n := c.Storage.ResolveNode(storageSpec.Nodes[0].Name)
	osd := OSDInfo{
		ID: 0,
	}
	deployment := c.makeOSDDeployment(n.Name, n.Devices, n.Selection, v1.ResourceRequirements{}, osd)
	assert.NotNil(t, deployment)

	// pod spec should have a volume for the given dir
	podSpec := deployment.Spec.Template.Spec
	assert.Equal(t, 2, len(podSpec.Volumes))
}

func TestStorageSpecConfig(t *testing.T) {
	storageSpec := rookalpha.StorageSpec{
		Config: rookalpha.Config{},
		Nodes: []rookalpha.Node{
			{
				Name: "node1",
				Config: rookalpha.Config{
					Location: "rack=foo",
					StoreConfig: rookalpha.StoreConfig{
						StoreType:      "bluestore",
						DatabaseSizeMB: 10,
						WalSizeMB:      20,
						JournalSizeMB:  30,
					},
				},
				Resources: v1.ResourceRequirements{
					Limits: v1.ResourceList{
						v1.ResourceCPU: *resource.NewQuantity(100.0, resource.BinarySI),
					},
					Requests: v1.ResourceList{
						v1.ResourceMemory: *resource.NewQuantity(1337.0, resource.BinarySI),
					},
				},
			},
		},
	}

	clientset := fake.NewSimpleClientset()
	c := New(&clusterd.Context{Clientset: clientset, ConfigDir: "/var/lib/rook", Executor: &exectest.MockExecutor{}}, "ns", "rook/rook:myversion",
		storageSpec, "", rookalpha.Placement{}, false, v1.ResourceRequirements{}, metav1.OwnerReference{})

	n := c.Storage.ResolveNode(storageSpec.Nodes[0].Name)

	job := c.makeJob(n.Name, n.Devices, n.Selection, c.Storage.Nodes[0].Resources, n.Config)
	assert.NotNil(t, job)

	container := job.Spec.Template.Spec.Containers[0]
	assert.NotNil(t, container)
	verifyEnvVar(t, container.Env, "ROOK_OSD_STORE", "bluestore", true)
	verifyEnvVar(t, container.Env, "ROOK_OSD_DATABASE_SIZE", strconv.Itoa(10), true)
	verifyEnvVar(t, container.Env, "ROOK_OSD_WAL_SIZE", strconv.Itoa(20), true)
	verifyEnvVar(t, container.Env, "ROOK_OSD_JOURNAL_SIZE", strconv.Itoa(30), true)
	verifyEnvVar(t, container.Env, "ROOK_LOCATION", "rack=foo", true)

	assert.Equal(t, "100", container.Resources.Limits.Cpu().String())
	assert.Equal(t, "1337", container.Resources.Requests.Memory().String())

	// verify that osd config can be discovered from the container and matches the original config from the spec
	cfg := getConfigFromContainer(container)
	assert.Equal(t, storageSpec.Nodes[0].Config, cfg)
}

func TestHostNetwork(t *testing.T) {
	storageSpec := rookalpha.StorageSpec{
		Config: rookalpha.Config{},
		Nodes: []rookalpha.Node{
			{
				Name: "node1",
				Config: rookalpha.Config{
					Location: "rack=foo",
					StoreConfig: rookalpha.StoreConfig{
						StoreType:      "bluestore",
						DatabaseSizeMB: 10,
						WalSizeMB:      20,
						JournalSizeMB:  30,
					},
				},
			},
		},
	}

	clientset := fake.NewSimpleClientset()
	c := New(&clusterd.Context{Clientset: clientset, ConfigDir: "/var/lib/rook", Executor: &exectest.MockExecutor{}}, "ns", "myversion",
		storageSpec, "", rookalpha.Placement{}, true, v1.ResourceRequirements{}, metav1.OwnerReference{})

	n := c.Storage.ResolveNode(storageSpec.Nodes[0].Name)
	osd := OSDInfo{
		ID: 0,
	}
	r := c.makeOSDDeployment(n.Name, n.Devices, n.Selection, v1.ResourceRequirements{}, osd)
	assert.NotNil(t, r)

	assert.Equal(t, true, r.Spec.Template.Spec.HostNetwork)
	assert.Equal(t, v1.DNSClusterFirstWithHostNet, r.Spec.Template.Spec.DNSPolicy)
}
