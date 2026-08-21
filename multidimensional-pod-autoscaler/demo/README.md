# MultidimPodAutoscaler — Demo

## Prerequisites

| Requirement | Notes |
|---|---|
| Kubernetes cluster | kind, minikube, or any real cluster |
| MPA CRD | `kubectl apply -f deploy/mpa-v1alpha1-crd-gen.yaml` |
| MPA controller running | `make deploy IMG=<your-image>` — see the root [README](../README.md) |
| VPA updater + admission-controller | Deployed with `MultidimPodAutoscaler=true` — see setup below |

### Quick kind setup

```bash
# 1 — cluster
kind create cluster

# 2 — VPA actuation components (updater + admission-controller) with the
#     MultidimPodAutoscaler feature gate enabled.
#     Run from the vertical-pod-autoscaler/ directory:
cd vertical-pod-autoscaler
FEATURE_GATES="MultidimPodAutoscaler=true" ./hack/deploy-for-e2e-locally.sh actuation
cd ..

# 3 — MPA controller
cd multidimensional-pod-autoscaler
make docker-build kind-load deploy
```

> **Why `actuation`?**  The `actuation` suite deploys the **updater** and the
> **admission-controller** — the only two VPA components that need the
> `MultidimPodAutoscaler` feature gate (see
> [`pkg/features/features.go`](../vertical-pod-autoscaler/pkg/features/features.go)).

Wait for all components to be ready:

```bash
kubectl -n kube-system rollout status deployment/vpa-updater
kubectl -n kube-system rollout status deployment/vpa-admission-controller
kubectl -n kube-system rollout status deployment/mpa-controller
```

---

## HPA preemption with VPA Recreate, coordinated by MPA

1. MPA gives the VPA the active token while the HPA is stable.  The patched
   recommendation causes the updater to evict a pod; the admission-controller
   recreates it with the new CPU request.
2. When the HPA status is patched to show scaling (`desired > current`) the MPA
   immediately pauses the VPA — no pod evictions compete with the scale-out.
3. Once the HPA is patched back to stable the MPA waits the
   `stableDurationSeconds` settling window, clears `activeAutoscaler`, then
   re-activates the VPA.

### Steps

#### 1. Deploy workload

```bash
kubectl apply -f demo/manifests/00-namespace.yaml
kubectl apply -f demo/manifests/01-deployment.yaml
kubectl -n mpa-demo rollout status deployment/hamster
```

The workload starts with a `100m` CPU request — deliberately below the patched
recommendation target of `250m` so the updater sees `OutsideRecommendedRange`
and evicts the pods immediately.

##### 2, Apply VPA, HPA, and MPA

```bash
kubectl apply -f demo/manifests/02-vpa-high.yaml
kubectl apply -f demo/manifests/04-hpa.yaml
kubectl apply -f demo/manifests/05-mpa.yaml
```

Confirm the MPA discovers both scalers:

```bash
kubectl get mpa -n mpa-demo hamster-mpa -oyaml|yq .status

# Expected:
autoscalers:
  - inProgress: false
    name: hamster-hpa
    scalerType: HorizontalPodAutoscaler
  - inProgress: false
    name: hamster-vpa-high
    scalerType: VerticalPodAutoscaler
conditions:
  - lastTransitionTime: "2026-08-21T07:57:02Z"
    message: MPA is the sole owner of spec.targetRef and is coordinating scalers
    reason: Coordinating
    status: "True"
    type: Active
lastTransitionTime: "2026-08-21T07:57:02Z"
```

#### 3. Inject a VPA recommendation

Patch the VPA
recommendation.  The MPA sees a stable HPA and a VPA with
`RecommendationProvided=True` and gives the VPA the active token:

```bash
# VPA recommendation (lowerBound > current request → OutsideRecommendedRange = true)
kubectl -n mpa-demo patch vpa hamster-vpa-high \
  --subresource=status --type=merge -p '{
    "status": {
      "recommendation": {
        "containerRecommendations": [{
          "containerName": "hamster",
          "target":     {"cpu": "250m", "memory": "105Mi"},
          "lowerBound": {"cpu": "200m", "memory": "100Mi"},
          "upperBound": {"cpu": "500m", "memory": "200Mi"}
        }]
      },
      "conditions": [{"type": "RecommendationProvided", "status": "True",
        "reason": "DemoPatched", "message": "Patched by demo"}]
    }
  }'
```

Watch the MPA activate the VPA and the updater evict a pod:

```bash
watch -n 5 '
echo "=== MPA active autoscaler ==="
kubectl -n mpa-demo get mpa hamster-mpa \
  -o jsonpath="{.status.activeAutoscaler}{\"\\n\"}"

echo ""
echo "=== VPA paused? ==="
kubectl -n mpa-demo get vpa hamster-vpa-high \
  -o jsonpath="{.spec.paused}{\"\\n\"}"

echo ""
echo "=== Pods ==="
kubectl -n mpa-demo get pods'
```

Expect:

```bash
=== MPA active autoscaler ===
{"inProgress":true,"name":"hamster-vpa-high","scalerType":"VerticalPodAutoscaler"}

=== VPA paused? ===
false

=== Pods ===
NAME                       READY   STATUS    RESTARTS   AGE
hamster-7bcd444f7b-7664p   1/1     Running   0          3m45s
hamster-7bcd444f7b-jd5h4   1/1     Running   0          3m45s
```

Once a replacement pod is Running, confirm its CPU request was raised:

```bash
NEW_POD=$(kubectl -n mpa-demo get pod -l app=hamster \
  --sort-by=.metadata.creationTimestamp -o name | tail -1)
kubectl -n mpa-demo get "$NEW_POD" \
  -o jsonpath='{.spec.containers[0].resources.requests}{"\n"}'
```

Expected: `map[cpu:250m memory:105Mi]`

#### 4. Drive CPU above 60 % to trigger real HPA scale-out (MPA preemption)**

Start the kubectl proxy and send load to every pod:

```bash
# Terminal 1

kubectl proxy --port=8001
```

```bash
# Terminal 2

# 200 millicores at 250m request = ~80 % utilisation, above the 60 % target
for POD in $(kubectl -n mpa-demo get pod -l app=hamster \
    -o jsonpath='{.items[*].metadata.name}'); do
  echo "http://localhost:8001/api/v1/namespaces/mpa-demo/pods/${POD}:8080/proxy/ConsumeCPU"
  curl -s -X POST \
    "http://localhost:8001/api/v1/namespaces/mpa-demo/pods/${POD}:8080/proxy/ConsumeCPU" \
    -d "millicores=200&durationSec=30" &
done
```

The HPA controller raises `desiredReplicas`.  The MPA detects `desired ≠
current` and hands the token to the HPA, pausing the VPA:

```bash
watch -n 5 '
echo "=== HPA ==="
kubectl -n mpa-demo get hpa hamster-hpa

echo ""
echo "=== MPA active autoscaler ==="
kubectl -n mpa-demo get mpa hamster-mpa \
  -o jsonpath="{.status.activeAutoscaler}{\"\\n\"}"

echo ""
echo "=== VPA paused? ==="
kubectl -n mpa-demo get vpa hamster-vpa-high \
  -o jsonpath="{.spec.paused}{\"\\n\"}"'
```

Expect:

- activeAutoscaler → hamster-hpa
- VPA spec.paused  → true

```text
=== HPA ===
NAME          REFERENCE            TARGETS        MINPODS   MAXPODS   REPLICAS   AGE
hamster-hpa   Deployment/hamster   cpu: 80%/60%   2         10        2          15m

=== MPA active autoscaler ===
{"inProgress":true,"name":"hamster-hpa","scalerType":"HorizontalPodAutoscaler"}

=== VPA paused? ===
true
```

#### 5. Stop the load; MPA returns the token to the VPA**

```bash
# Cancel the load early (or wait for durationSec=300):
for POD in $(kubectl -n mpa-demo get pod -l app=hamster \
    -o jsonpath='{.items[*].metadata.name}'); do
  curl -s -X POST \
    "http://localhost:8001/api/v1/namespaces/mpa-demo/pods/${POD}:8080/proxy/ConsumeCPU" \
    -d "millicores=0&durationSec=1" &
done
```

Once the HPA scales back in (`desiredReplicas == currentReplicas`) the MPA
starts the `stableDurationSeconds: 30` settling window, then re-activates the
VPA:

```bash
=== HPA ===
NAME          REFERENCE            TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
hamster-hpa   Deployment/hamster   cpu: 0%/60%   2         10        3          17m

=== MPA active autoscaler ===
{"inProgress":true,"name":"hamster-vpa-high","scalerType":"VerticalPodAutoscaler"}

=== VPA paused? ===
false
```

**Cleanup**

```bash
# Delete the MPA first so its finalizer can release VPAs before they are removed.
kubectl -n mpa-demo delete mpa --all --ignore-not-found
kubectl -n mpa-demo delete vpa --all --ignore-not-found
kubectl -n mpa-demo delete hpa --all --ignore-not-found
kubectl -n mpa-demo delete deployment hamster --ignore-not-found
kubectl delete namespace mpa-demo --ignore-not-found

make undeploy
```
