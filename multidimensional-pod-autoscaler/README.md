# MultidimPodAutoscaler (MPA)

MPA is a Kubernetes controller that coordinates **HPA and VPA on the same workload**,
preventing the two scalers from fighting each other.

When an HPA is actively scaling (desired ≠ current replicas), MPA pauses all VPAs
for that workload. Once the fleet stabilises, MPA waits a configurable *stable window*
before handing the token back to a VPA. If multiple VPAs target the same workload,
MPA rotates the active token among them.

## Quick start with KinD

1. Create KinD cluster

    ```sh
    kind create cluster
    ```

2. Build and deploy

    ```sh
    make docker-build kind-load deploy
    ```

3. Wait for all components to be ready:

    ```bash
    kubectl -n kube-system rollout status deployment/vpa-updater
    kubectl -n kube-system rollout status deployment/vpa-admission-controller
    kubectl -n kube-system rollout status deployment/mpa-controller
    ```

4. Deploy sample

    ```sh
    kubectl apply -f demo/manifests/quick-sample.yaml
    ```

    Expect:

    ```sh
    > kubectl get mpa -n mpa-demo
    NAME          TARGET KIND   TARGET NAME   ACTIVE AUTOSCALER   AGE
    hamster-mpa   Deployment    hamster                           8s
    ```

5. Stress the app

    Termnial 1:

    ```sh
    kubectl proxy --port=8001
    ```

    Terminal 2:

    ```sh
    curl -s -X POST \
    "http://localhost:8001/api/v1/namespaces/mpa-demo/pods/$(kubectl -n mpa-demo get pod -l app=hamster \
    -o jsonpath='{.items[*].metadata.name}'):8080/proxy/ConsumeCPU" \
    -d "millicores=200&durationSec=30"
    ```

    Wait for the HPA observes the load:

    ```sh
    > watch kubectl get hpa -n mpa-demo
    ```

    Soon after, MPA will set the HPA to active autoscaler.

    ```sh
    > watch kubectl get mpa -n mpa-demo                   
    NAME          TARGET KIND   TARGET NAME   ACTIVE AUTOSCALER   AGE
    hamster-mpa   Deployment    hamster       hamster-hpa         3m17s
    ```

For full integration demo with VPA, check out [demo](./demo/README.md).

## Development

```bash
# Run all tests
make test

# Regenerate CRD YAML after changing pkg/apis/...
make generate

# Verify nothing is out of date before opening a PR
make vet verify-crd
```
