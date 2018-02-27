// Package mon for the Ceph monitors.
package mon

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/ghodss/yaml"

	"github.com/rook/rook/pkg/operator/k8sutil"
	"k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/kubelet/apis"
)

const (
	//TODO read template from configmap data
	monTemplate = `
apiVersion: extensions/v1beta1
kind: ReplicaSet
metadata:
  labels: {{ .RSLabels }}
  name: {{ .RSName }}
  namespace: {{ .RSNamespace }}
  ownerReferences: {{ .RSOwner }}
spec:
  replicas: {{ .Replica }}
  selector:
    matchLabels: {{ .NodeSelector }}
  template:
    metadata:
     labels: {{ .PodLabels }}
     name: {{ .PodName }}
     namespace: {{ .PodNamespace }}
    spec:
      affinity: {}
      containers:
      - args: {{ .Args }}
        env: {{ .Env }}
        image:  {{ .Image }} 
        imagePullPolicy: IfNotPresent
        name: {{ .ContainerName }}
        ports: {{ .Ports }}
        resources: {{ .Resources }}
        volumeMounts: {{ .Mounts }}
      dnsPolicy: {{ .DNSPolicy }}
      nodeSelector: {{ .NodeSelector }}
      restartPolicy: Always
      hostNetwork: {{ .HostNetwork }}
      schedulerName: default-scheduler
      securityContext: {}
      terminationGracePeriodSeconds: 30
      volumes: {{ .Volumes }}
`
)

type monParams struct {
	RSLabels      map[string]string
	RSName        string
	RSNamespace   string
	RSOwner       []metav1.OwnerReference
	Replica       *int32
	PodName       string
	PodNamespace  string
	PodLabels     map[string]string
	Args          []string
	Image         string
	ContainerName string
	Ports         []v1.ContainerPort
	Mounts        []v1.VolumeMount
	DNSPolicy     v1.DNSPolicy
	NodeSelector  map[string]string
	Env           []v1.EnvVar
	Volumes       []v1.Volume
	Resources     v1.ResourceRequirements
	HostNetwork   bool
}

func makeMonTemplate(param *monParams) (string, error) {
	var writer bytes.Buffer
	t := template.New("mon")
	err := template.Must(t.Parse(monTemplate)).Execute(&writer, param)
	return writer.String(), err
}

func (c *Cluster) makeReplicaSetFromTemplate(config *monConfig, hostname string) *extensions.ReplicaSet {
	var rs extensions.ReplicaSet

	replicaCount := int32(1)
	dataDirSource := v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}
	if c.dataDirHostPath != "" {
		dataDirSource = v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: c.dataDirHostPath}}
	}

	param := &monParams{
		RSLabels:     c.getLabels(config.Name),
		RSName:       config.Name,
		RSNamespace:  c.Namespace,
		RSOwner:      []metav1.OwnerReference{c.ownerRef},
		Replica:      &replicaCount,
		NodeSelector: map[string]string{apis.LabelHostname: hostname},
		Volumes: []v1.Volume{
			{Name: k8sutil.DataDirVolume, VolumeSource: dataDirSource},
			k8sutil.ConfigOverrideVolume(),
		},
		HostNetwork:  c.HostNetwork,
		PodNamespace: c.Namespace,
		PodLabels:    c.getLabels(config.Name),
		Args: []string{
			"mon",
			fmt.Sprintf("--config-dir=%s", k8sutil.DataDir),
			fmt.Sprintf("--name=%s", config.Name),
			fmt.Sprintf("--port=%d", config.Port),
			fmt.Sprintf("--fsid=%s", c.clusterInfo.FSID),
		},
		PodName: appName,
		Image:   k8sutil.MakeRookImage(c.Version),
		Ports: []v1.ContainerPort{
			{
				Name:          "client",
				ContainerPort: config.Port,
				Protocol:      v1.ProtocolTCP,
			},
		},
		Mounts: []v1.VolumeMount{
			{Name: k8sutil.DataDirVolume, MountPath: k8sutil.DataDir},
			k8sutil.ConfigOverrideMount(),
		},
		Env: []v1.EnvVar{
			k8sutil.PodIPEnvVar(k8sutil.PrivateIPEnvVar),
			PublicIPEnvVar(config.PublicIP),
			ClusterNameEnvVar(c.Namespace),
			EndpointEnvVar(),
			SecretEnvVar(),
			AdminSecretEnvVar(),
			k8sutil.ConfigOverrideEnvVar(),
		},
		Resources: c.resources,
	}

	if c.HostNetwork {
		param.DNSPolicy = v1.DNSClusterFirstWithHostNet
	}

	t, err := makeMonTemplate(param)
	if err != nil {
		logger.Debugf("failed to make mon template: err %+v", err)
		return nil
	}

	err = yaml.Unmarshal([]byte(t), &rs)
	if err != nil {
		logger.Debugf("failed to unmarshal into rs: err %+v", err)
		return nil
	}

	return &rs
}
