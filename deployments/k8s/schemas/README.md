# Pinned offline Kubernetes schemas

This is a **static release-template baseline**, not a supported-cluster declaration
or production acceptance. The actual cluster still needs the documented
`kubectl apply --dry-run=server` admission check and rollout/startup verification.
OpenAPI/JSON Schema cannot prove controller validation, selector consistency,
policy/RBAC, Secret availability, image retrieval, storage provisioning or health.

## Provenance and integrity

- Baseline: Kubernetes **v1.34.1**, `standalone-strict` JSON schemas derived from
  Kubernetes OpenAPI by `yannh/kubernetes-json-schema`.
- Source repository: <https://github.com/yannh/kubernetes-json-schema>.
- Source commit: **`817113f21a2643311cbc3e992bb44ffe1c416c54`**.
- Source directory: `v1.34.1-standalone-strict/`.
- The nine JSON files are **byte-identical** to that commit; descriptions and
  arbitrary-map schemas are retained. They are not locally written miniature
  schemas. `LICENSE` retains the upstream Apache-2.0 notice.
- `../schema_pins_test.go` pins every file's SHA-256 and the **full GVK** independently
  of schema annotations. A missing, changed or oversized schema fails. Adding a
  kind or changing the baseline requires reviewed bytes, pins and rejection tests;
  there is no default URL, runtime schema download, `ignore-missing` or skip.
- Validator: `github.com/santhosh-tekuri/jsonschema/v6` **v6.0.2**, pinned in the
  main `go.mod`/`go.sum` and imported only by deployment tests. Its draft baseline
  is explicitly 2020-12; embedded standard metaschemas resolve without networking.
  The compiler has an empty external URL loader: HTTP(S) and arbitrary local file
  references are rejected. Vendored schemas contain no external `$ref`.

| Full GVK | File | SHA-256 |
|---|---|---|
| `v1/Namespace` | `namespace-v1.json` | `324fae677b98d1a6d54340db0c334d053e8ffbafceb3f73326e41de2610d5843` |
| `v1/ConfigMap` | `configmap-v1.json` | `e0eaddebd677c08aa092b2da2264d86ac4fc34eed112b9fac2945b3f00c1e9b1` |
| `v1/Service` | `service-v1.json` | `8bf019854daed511e7c174896a898173fa65d88ec5937c687a37303d4cc9351b` |
| `apps/v1/Deployment` | `deployment-apps-v1.json` | `92b7a333a49124f5300095d3e7786ca2038b7d6e602c309f73e90631377e4b0a` |
| `apps/v1/StatefulSet` | `statefulset-apps-v1.json` | `e095a9d88776fbb2e9c9312baac322c992a9f5188f81ae690bb7dc00fc3915fa` |
| `autoscaling/v2/HorizontalPodAutoscaler` | `horizontalpodautoscaler-autoscaling-v2.json` | `613e60f54bc1d70776eb3e75fbdbc2f1250b5c1a92c88f54846c5fabd02f6f3e` |
| `policy/v1/PodDisruptionBudget` | `poddisruptionbudget-policy-v1.json` | `da73f50ad0264d73f668eecaa9959da65afe2846602a7a5c8fd51f7799d6a258` |
| `networking.k8s.io/v1/Ingress` | `ingress-networking-v1.json` | `4e0f63ad84c2bf22565e489d1f4b885ddaa9f6bf7cff1ddd562553760afe4d79` |
| `batch/v1/Job` | `job-batch-v1.json` | `967234ab9eb5477fd8e5538c83c85817a560aecc1aaf886fafbe8a4ac0972023` |

## Verification and updates

```bash
make check-k8s-manifest
go test ./deployments/k8s -count=1
```

The mandatory gate validates the **actual five shell-rendered artifacts**, not just
another render of the known-good source: steady14 + paused13 + migration up/down/
version1 each = **30 resources**. No empty document, duplicate resource, partial
bundle or unsupported full GVK is accepted. Paused and steady documents are compared
in full: only HPA removal and setting the exact API/worker replicas to zero may differ.

Every invocation also checks derived invalid manifests (unknown fields/GVKs, bad
numbers/values/args, duplicate keys and missing required fields), changed/missing
schema files, denied external resolvers and bounded manifest sizes/document counts.
The schema checker is in the existing `deployments/k8s` Go test package, not a new
production binary or Kubernetes client. A cold module cache may need normal verified
Go dependency provisioning; after provisioning schema validation is wholly local.
The shell gate bounds the whole Go command to two minutes and its tests to one minute.

For an intentional baseline update, fetch each exact source path over verified HTTPS
from an immutable commit, retain its license/provenance, compare file hashes, review
all schema changes, update the GVK/hash pins and rerun the complete backend gate.
Do not fetch `master` during validation or silently use the operator's kubeconfig,
an ambient validator binary or a different schema/cluster version to make it pass.
The rendered release must still be checked against its real target cluster.
