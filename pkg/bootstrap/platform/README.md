# AWS platform bootstrap (`istio-proxy` on EKS)

On Amazon EC2, `istio-proxy` normally reads region, zone, and instance id from the **instance metadata service (IMDS)**. On Kubernetes (including EKS), many pods share the same node; concurrent IMDSv2 session-token (`PUT /latest/api/token`) calls can be **throttled**, which can slow or destabilize proxy startup.

Istio can skip IMDS for AWS metadata when the proxy container has the right **environment variables** set. This document describes how to configure that on **EKS**.

## Behavior summary

| Variable | Role |
|----------|------|
| `AWS_REGION` | If set, AWS platform detection does **not** call IMDS for `iam/info`. Often already present when using **IAM Roles for Service Accounts (IRSA)**. |
| `AWS_AVAILABILITY_ZONE` | Availability zone used for locality (replaces `placement/availability-zone` from IMDS). |
| `K8S_NODE_NAME` | Node identity used for `aws_instance_id` metadata (typically the node name, not the EC2 instance id). Replaces `instance-id` from IMDS. |

**Full IMDS bypass (no token `PUT`, no metadata `GET`s)** applies only when **all three** variables are non-empty. If only some are set, Istio fills the rest from IMDS as before.

## EKS: `AWS_REGION` (IRSA)

When you use IRSA, annotate the workload `ServiceAccount` with an IAM role. The EKS Pod Identity / IRSA webhook injects `AWS_REGION` (and related AWS variables) into containers that use that service account.

Example:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-sa
  namespace: my-ns
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/my-iam-role
```

Ensure the pod's containers (including `istio-proxy`) run with this service account so `AWS_REGION` is present. If `AWS_REGION` is missing, platform detection still uses IMDS unless you set the proxy environment variable `CLOUD_PLATFORM=aws` (or another supported platform value) so Istio skips discovery.

Reference: [IAM roles for service accounts](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html).

## EKS: `K8S_NODE_NAME`

Expose the Kubernetes node name to the proxy using the downward API:

```yaml
env:
  - name: K8S_NODE_NAME
    valueFrom:
      fieldRef:
        fieldPath: spec.nodeName
```

Add this under the **`istio-proxy`** container (ingress gateway, egress gateway, or workload deployment).

## EKS: `AWS_AVAILABILITY_ZONE`

There is no single standard field on every pod that always contains the zone. You must ensure the proxy sees a zone string; common approaches:

1. **Pod label** (if your cluster or admission controller sets `topology.kubernetes.io/zone` on the pod):

   ```yaml
   env:
     - name: AWS_AVAILABILITY_ZONE
       valueFrom:
         fieldRef:
           fieldPath: metadata.labels['topology.kubernetes.io/zone']
   ```

2. **Mesh-wide injection** via policy (e.g. Kyverno, OPA Gatekeeper, or a custom mutating webhook) that copies zone from the **node** onto the pod or injects env vars.

3. **Per-node-pool / per-zone Helm values** if deployments are pinned to one AZ.

If `AWS_AVAILABILITY_ZONE` is empty, Istio will still try to read the zone from IMDS (subject to hop limit and throttling).

## Example: ingress gateway container snippet

Combine the three env entries on the `istio-proxy` container (order does not matter):

```yaml
containers:
  - name: istio-proxy
    env:
      - name: K8S_NODE_NAME
        valueFrom:
          fieldRef:
            fieldPath: spec.nodeName
      - name: AWS_AVAILABILITY_ZONE
        valueFrom:
          fieldRef:
            fieldPath: metadata.labels['topology.kubernetes.io/zone']
      # AWS_REGION often comes from IRSA on the pod service account; if not, set it explicitly:
      # - name: AWS_REGION
      #   value: us-west-2
```

Adjust `AWS_AVAILABILITY_ZONE` sourcing to match how your cluster exposes zone to the pod.

## Sidecar-injected workloads

Injected pods need the same three variables on the **`istio-proxy`** container. Typical approaches:

- Patch the workload template (Deployment, etc.) with the `env` entries above.
- Use your organization's standard mutating admission to inject these env vars for namespaces where Istio sidecars run.

`ISTIO_META_*` metadata does **not** replace these variables; bootstrap reads **`AWS_REGION`**, **`AWS_AVAILABILITY_ZONE`**, and **`K8S_NODE_NAME`** explicitly.

## Verify

After rollout, confirm the proxy sees the variables (names may vary if your shell strips empty values):

```bash
kubectl exec -n istio-system deploy/istio-ingressgateway -c istio-proxy -- \
  printenv AWS_REGION AWS_AVAILABILITY_ZONE K8S_NODE_NAME
```

## Related AWS guidance

- [EKS best practices - IAM](https://docs.aws.amazon.com/eks/latest/best-practices/identity-and-access-management.html) (IMDSv2, hop limit).
- [Configure the instance metadata service](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-instance-metadata-service.html).

If pods cannot reach IMDS (for example hop limit `1` while using bridge networking), supplying all three environment variables avoids depending on the metadata endpoint for bootstrap.
