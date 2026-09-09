# 🚶 GetOffYourSaaS

A kubernetes operator that makes you get off your ass. The more steps you walk, the more CPU for your workloads.

![Build Status](https://github.com/circa10a/getoffyoursaas/workflows/deploy/badge.svg)
![GitHub release (latest by date)](https://img.shields.io/github/v/release/circa10a/getoffyoursaas)

<img width="40%" src="https://raw.githubusercontent.com/ashleymcnamara/gophers/refs/heads/master/CouchPotatoGopher.png" align="right"/>

- [How it works](#how-it-works)
- [Example spec](#example-spec)
- [Install](#install)
  - [Kubectl](#kubectl)
  - [Helm](#helm)
- [Sending step counts](#sending-step-counts)
  - [Apple Shortcut](#apple-shortcut)
- [Configuration options](#configuration-options)
- [Development](#development)

### How it works

```
Apple Shortcut POST /v1/steps => operator => minAllowed.cpu => VPA => your pods
```

Your step count becomes a CPU floor, linear between `minCPU` at zero steps and `maxCPU` at `dailyStepGoal`:

```
floor = minCPU + (steps / dailyStepGoal) * (maxCPU - minCPU)
```

7431 steps against the spec below earns `768m`. Steps past the goal are clamped. VPA's recommender then clamps its own recommendation up to that floor, so pods are never starved below what they actually need.

Apple Health doesn't sync to macOS, which is why readings get pushed in from your phone instead of read off the host.

### Example spec

```yaml
apiVersion: getoffyoursaas.io/v1alpha1
kind: StepScaler
metadata:
  name: lazy-app
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: lazy-app
  containerName: "*"
  dailyStepGoal: 10000
  minCPU: 100m
  maxCPU: 1000m
```

```console
$ kubectl get stepscalers
NAME       TARGET     STEPS   CPU    GOAL
lazy-app   lazy-app   7431    768m   10000
```

### Install

> [!IMPORTANT]
> The [Vertical Pod Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler) has to be installed first. Without its CRD the manager fails to start and you get `CrashLoopBackOff`.

#### Kubectl

```console
kubectl apply -f https://raw.githubusercontent.com/circa10a/getoffyoursaas/main/dist/install.yaml
```

#### Helm

```console
helm install getoffyoursaas oci://registry-1.docker.io/circa10a/getoffyoursaas
```

### Sending step counts

`POST /v1/steps` on container port `8080`, exposed as a NodePort on `30080`. There's no auth, so keep it on a network you trust.

```console
curl -X POST http://<host>:30080/v1/steps \
  -H 'Content-Type: application/json' \
  -d '{"steps": 7431}'
```

`steps` is required and must be 0 to 200000. `asOf` is an optional RFC3339 timestamp, defaulting to now. Returns `204`, or `400` on a bad body. The reading applies to every StepScaler in the cluster.

#### Apple Shortcut

Get the host address. On a local kind cluster that's your machine's LAN IP, since the node port is published onto the host:

```console
ipconfig getifaddr en0
```

Then build a shortcut with three actions.

**1. Find Health Samples**

| Field | Value |
|---|---|
| Type | `Steps` |
| Filter | `Start Date` `is today` |
| Unit | `count` |
| Group by | leave default |
| Sort by | leave default |
| Order | leave default |
| Limit | leave off |

Only `Type`, `Filter` and `Unit` change the result. `Sort by` and `Order` can't affect a sum, and `Limit` caps how many samples come back rather than narrowing the date range, so setting it undercounts your day.

**2. Calculate Statistics**

| Field | Value |
|---|---|
| Operation | `Sum` |
| Input | `Health Samples` |

Steps is a cumulative quantity in HealthKit, so summing the samples is the right aggregation.

**3. Get Contents of URL**

| Field | Value |
|---|---|
| URL | `http://<host>:30080/v1/steps` |
| Method | `POST` |
| Request Body | `JSON` |
| Body field | `steps`, type `Number`, set to the sum from step 2 |

Grant Health access when prompted, then add a Personal Automation on a Time of Day trigger to run when you'd like. To run very frequently, you have to cruft a trigger for when any alarm goes off and set a bunch of alarms.

> [!TIP]
> Health only writes new step samples every 15 minutes or so, so running the automation more often than that won't get you fresher numbers.

Check it landed:

```console
kubectl get stepscalers
```

### Configuration options

```console
  -enable-http2
        If set, HTTP/2 will be enabled for the metrics and webhook servers
  -health-probe-bind-address string
        The address the probe endpoint binds to. (default ":8081")
  -ingest-addr string
        address the step ingest endpoint binds to (default ":8080")
  -kubeconfig string
        Paths to a kubeconfig. Only required if out-of-cluster.
  -leader-elect
        Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.
  -metrics-bind-address string
        The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service. (default "0")
  -metrics-cert-key string
        The name of the metrics server key file. (default "tls.key")
  -metrics-cert-name string
        The name of the metrics server certificate file. (default "tls.crt")
  -metrics-cert-path string
        The directory that contains the metrics server certificate.
  -metrics-secure
        If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead. (default true)
  -webhook-cert-key string
        The name of the webhook key file. (default "tls.key")
  -webhook-cert-name string
        The name of the webhook certificate file. (default "tls.crt")
  -webhook-cert-path string
        The directory that contains the webhook certificate.
  -zap-devel
        Development Mode defaults(encoder=consoleEncoder,logLevel=Debug,stackTraceLevel=Warn). Production Mode defaults(encoder=jsonEncoder,logLevel=Info,stackTraceLevel=Error) (default true)
  -zap-encoder value
        Zap log encoding (one of 'json' or 'console')
  -zap-log-level value
        Zap Level to configure the verbosity of logging. Can be one of 'debug', 'info', 'error', 'panic' or any integer value > 0 which corresponds to custom debug levels of increasing verbosity
  -zap-stacktrace-level value
        Zap Level at and above which stacktraces are captured (one of 'info', 'error', 'panic').
  -zap-time-encoding value
        Zap time encoding (one of 'epoch', 'millis', 'nano', 'iso8601', 'rfc3339' or 'rfc3339nano'). Defaults to 'epoch'.
```

### Development

Bring up a kind cluster with the ingest port published, install VPA, then build and deploy:

```console
kind create cluster --config hack/kind-config.yaml

helm repo add fairwinds-stable https://charts.fairwinds.com/stable
helm install vpa fairwinds-stable/vpa \
  --namespace vpa --create-namespace \
  --set metrics-server.enabled=true \
  --set metrics-server.args="{--kubelet-insecure-tls}" \
  --wait

make local
```

`--kubelet-insecure-tls` is needed on kind. Without it metrics-server rejects kubelet's self-signed cert and VPA never produces recommendations.

#### Install a sample StepScaler

```console
make sample
```

Send it some steps and watch the floor move:

```console
curl -X POST "http://$(ipconfig getifaddr en0):30080/v1/steps" \
  -H 'Content-Type: application/json' -d '{"steps": 7431}'

kubectl get vpa lazy-app -o jsonpath='{.spec.resourcePolicy.containerPolicies[0].minAllowed.cpu}'
```

VPA's own recommendation follows within a minute or so.

#### Running tests

```console
make test      # unit and envtest
make lint
make test-e2e  # end to end against a throwaway kind cluster
```
