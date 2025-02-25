//go:build e2e
// +build e2e

package stan_test

import (
	"fmt"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes"

	. "github.com/kedacore/keda/v2/tests/helper"
)

// Load environment variables from .env file
var _ = godotenv.Load("../../.env")

const (
	testName = "stan-test"
)

var (
	testNamespace         = fmt.Sprintf("%s-ns", testName)
	deploymentName        = fmt.Sprintf("%s-deployment", testName)
	publishDeploymentName = fmt.Sprintf("%s-publish", testName)
	scaledObjectName      = fmt.Sprintf("%s-so", testName)
	stanServerName        = "stan-nats"
	minReplicaCount       = 0
	maxReplicaCount       = 5
)

type templateData struct {
	TestNamespace         string
	DeploymentName        string
	PublishDeploymentName string
	ScaledObjectName      string
	StanServerName        string
	MinReplicaCount       int
	MaxReplicaCount       int
}

type templateValues map[string]string

const (
	deploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.DeploymentName}}
  namespace: {{.TestNamespace}}
  labels:
    app.kubernetes.io/name: sub
    helm.sh/chart: sub-0.0.3
    app.kubernetes.io/instance: sub
    app.kubernetes.io/version: "0.0.3"
    app.kubernetes.io/managed-by: Helm
spec:
  replicas: 0
  selector:
    matchLabels:
      app.kubernetes.io/name: sub
      app.kubernetes.io/instance: sub
  template:
    metadata:
      labels:
        app.kubernetes.io/name: sub
        app.kubernetes.io/instance: sub
    spec:
      containers:
        - name: sub
          image: "balchu/gonuts-sub:c02e4ee"
          imagePullPolicy: Always
          command: ["/app"]
          args: ["-d", "5000", "-s", "nats://{{.StanServerName}}.{{.TestNamespace}}:4222","-d","10","--durable","ImDurable", "--qgroup", "grp1", "Test"]
`

	publishDeploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.PublishDeploymentName}}
  namespace: {{.TestNamespace}}
  labels:
    app.kubernetes.io/name: pub
    helm.sh/chart: pub-0.0.3
    app.kubernetes.io/instance: pub
    app.kubernetes.io/version: "0.0.3"
    app.kubernetes.io/managed-by: Helm
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: pub
      app.kubernetes.io/instance: pub
  template:
    metadata:
      labels:
        app.kubernetes.io/name: pub
        app.kubernetes.io/instance: pub
    spec:
      containers:
        - name: pub
          image: "balchu/gonuts-pub:c02e4ee-dirty"
          imagePullPolicy: Always
          command: ["/app"]
          args: ["-s", "nats://{{.StanServerName}}.{{.TestNamespace}}:4222", "-d", "10", "-limit", "1000", "Test"]
`

	lowLevelPublishDeploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.PublishDeploymentName}}2
  namespace: {{.TestNamespace}}
  labels:
    app.kubernetes.io/name: pub
    helm.sh/chart: pub-0.0.3
    app.kubernetes.io/instance: pub
    app.kubernetes.io/version: "0.0.3"
    app.kubernetes.io/managed-by: Helm
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: pub
      app.kubernetes.io/instance: pub
  template:
    metadata:
      labels:
        app.kubernetes.io/name: pub
        app.kubernetes.io/instance: pub
    spec:
      containers:
        - name: pub
          image: "balchu/gonuts-pub:c02e4ee-dirty"
          imagePullPolicy: Always
          command: ["/app"]
          args: ["-s", "nats://{{.StanServerName}}.{{.TestNamespace}}:4222", "-d", "10", "-limit", "10", "Test"]
`

	// Source: nats-ss/templates/service.yaml
	stanServiceTemplate = `apiVersion: v1
kind: Service
metadata:
  name: {{.StanServerName}}
  namespace: {{.TestNamespace}}
  labels:
    app: {{.StanServerName}}
    chart: nats-ss-0.0.1
    release: stan
    heritage: Helm
spec:
  type: ClusterIP
  ports:
    - name: client
      port: 4222
      targetPort: 4222
      protocol: TCP
    - name: monitor
      port: 8222
      targetPort: 8222
      protocol: TCP
  selector:
    app: {{.StanServerName}}
    release: stan
`

	// Source: nats-ss/templates/statefulset.yaml
	stanStatefulSetTemplate = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{.StanServerName}}
  namespace: {{.TestNamespace}}
  labels:
    app: {{.StanServerName}}
    chart: nats-ss-0.0.1
    release: stan
    heritage: Helm
spec:
  serviceName: {{.StanServerName}}
  replicas: 1
  selector:
    matchLabels:
      app: {{.StanServerName}}
  template:
    metadata:
      labels:
        app: {{.StanServerName}}
        release: stan
    spec:
      containers:
      - name: nats-ss
        image: nats-streaming:0.16.2
        imagePullPolicy: IfNotPresent
        command:
          - /nats-streaming-server
        args:
          - -m=8222
          - -st=FILE
          - --dir=/nats-datastore
          - --cluster_id=local-stan
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        volumeMounts:
        - mountPath: /nats-datastore
          name: nats-datastore
      volumes:
      - name: nats-datastore
        emptyDir: {}
`

	scaledObjectTemplate = `apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: {{.ScaledObjectName}}
  namespace: {{.TestNamespace}}
spec:
  scaleTargetRef:
    name: {{.DeploymentName}}
  minReplicaCount: {{.MinReplicaCount}}
  maxReplicaCount: {{.MaxReplicaCount}}
  pollingInterval: 3
  cooldownPeriod:  10
  triggers:
  - type: stan
    metadata:
      natsServerMonitoringEndpoint: "{{.StanServerName}}.{{.TestNamespace}}:8222"
      queueGroup: "grp1"
      durableName: "ImDurable"
      subject: "Test"
      lagThreshold: "10"
      activationLagThreshold: "15"
`
)

func TestStanScaler(t *testing.T) {
	// Create kubernetes resources
	kc := GetKubernetesClient(t)
	data, templates := getTemplateData()
	CreateKubernetesResources(t, kc, testNamespace, data, templates)

	assert.True(t, WaitForStatefulsetReplicaReadyCount(t, kc, stanServerName, testNamespace, 1, 60, 3),
		"replica count should be %d after 3 minutes", minReplicaCount)
	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, testNamespace, minReplicaCount, 60, 3),
		"replica count should be %d after 3 minutes", minReplicaCount)

	testActivation(t, kc, data)
	testScaleUp(t, kc, data)
	testScaleDown(t, kc)

	// cleanup
	DeleteKubernetesResources(t, kc, testNamespace, data, templates)
}

func testActivation(t *testing.T, kc *kubernetes.Clientset, data templateData) {
	t.Log("--- testing activation ---")
	templateTriggerJob := templateValues{"lowLevelPublishDeploymentTemplate": lowLevelPublishDeploymentTemplate}
	KubectlApplyMultipleWithTemplate(t, data, templateTriggerJob)

	AssertReplicaCountNotChangeDuringTimePeriod(t, kc, deploymentName, testNamespace, minReplicaCount, 60)
}

func testScaleUp(t *testing.T, kc *kubernetes.Clientset, data templateData) {
	t.Log("--- testing scale up ---")
	templateTriggerDeployment := templateValues{"publishDeploymentTemplate": publishDeploymentTemplate}
	KubectlApplyMultipleWithTemplate(t, data, templateTriggerDeployment)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, testNamespace, maxReplicaCount, 60, 3),
		"replica count should be %d after 3 minutes", maxReplicaCount)
}

func testScaleDown(t *testing.T, kc *kubernetes.Clientset) {
	t.Log("--- testing scale down ---")
	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, testNamespace, minReplicaCount, 60, 3),
		"replica count should be %d after 3 minutes", minReplicaCount)
}

func getTemplateData() (templateData, map[string]string) {
	return templateData{
			TestNamespace:         testNamespace,
			DeploymentName:        deploymentName,
			PublishDeploymentName: publishDeploymentName,
			ScaledObjectName:      scaledObjectName,
			StanServerName:        stanServerName,
			MinReplicaCount:       minReplicaCount,
			MaxReplicaCount:       maxReplicaCount,
		}, templateValues{
			"stanServiceTemplate":     stanServiceTemplate,
			"stanStatefulSetTemplate": stanStatefulSetTemplate,
			"deploymentTemplate":      deploymentTemplate,
			"scaledObjectTemplate":    scaledObjectTemplate,
		}
}
