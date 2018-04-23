/*
Copyright 2018 The Rook Authors. All rights reserved.

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

// Package discover to discover devices on storage nodes.
package discover

import (
	"os"
	"testing"

	"github.com/rook/rook/pkg/operator/k8sutil"
	"github.com/rook/rook/pkg/operator/test"
	"github.com/stretchr/testify/assert"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStartDiscoveryDaemonset(t *testing.T) {
	clientset := test.New(3)

	os.Setenv(k8sutil.PodNamespaceEnvVar, "rook-system")
	defer os.Unsetenv(k8sutil.PodNamespaceEnvVar)

	os.Setenv(k8sutil.PodNameEnvVar, "rook-operator")
	defer os.Unsetenv(k8sutil.PodNameEnvVar)

	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rook-operator",
			Namespace: "rook-system",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "mypodContainer",
					Image: "rook/test",
				},
			},
		},
	}
	clientset.CoreV1().Pods("rook-system").Create(&pod)

	namespace := "ns"
	a := New(clientset)

	// start a basic cluster
	err := a.Start(namespace, "rook/rook:myversion")
	assert.Nil(t, err)

	// check clusters rbac roles
	_, err = clientset.CoreV1().ServiceAccounts(namespace).Get("rook-discover", metav1.GetOptions{})
	assert.Nil(t, err)

	_, err = clientset.RbacV1beta1().Roles(namespace).Get("rook-discover", metav1.GetOptions{})
	assert.Nil(t, err)

	// check daemonset parameters
	agentDS, err := clientset.Extensions().DaemonSets(namespace).Get("rook-discover", metav1.GetOptions{})
	assert.Nil(t, err)
	assert.Equal(t, namespace, agentDS.Namespace)
	assert.Equal(t, "rook-discover", agentDS.Name)
	assert.True(t, *agentDS.Spec.Template.Spec.Containers[0].SecurityContext.Privileged)
	volumes := agentDS.Spec.Template.Spec.Volumes
	assert.Equal(t, 2, len(volumes))
	volumeMounts := agentDS.Spec.Template.Spec.Containers[0].VolumeMounts
	assert.Equal(t, 2, len(volumeMounts))
	envs := agentDS.Spec.Template.Spec.Containers[0].Env
	assert.Equal(t, 2, len(envs))
	image := agentDS.Spec.Template.Spec.Containers[0].Image
	assert.Equal(t, "rook/rook:myversion", image)
	assert.Nil(t, agentDS.Spec.Template.Spec.Tolerations)
}
