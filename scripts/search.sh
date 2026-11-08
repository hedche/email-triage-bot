#!/usr/bin/env bash
# Searches the archive bucket, which only the cluster can reach, by running
# `email-triage-bot search` once in a throwaway pod. Arguments are passed through:
#
#   scripts/search.sh --from example.com --since 2026-11-01
#   scripts/search.sh --ref 0123456789ab
#
# The bucket credentials reach the pod through envFrom and are never read here.
# Needs kubectl pointed at the cluster (KUBECONFIG) and jq. SEARCH_IMAGE overrides
# the image, which defaults to the one the commands Deployment is running.
set -euo pipefail

namespace=email-triage-bot
deadline=300

for tool in kubectl jq; do
  command -v "$tool" >/dev/null || { echo "search.sh: $tool is required" >&2; exit 1; }
done

image=${SEARCH_IMAGE:-$(kubectl -n "$namespace" get deployment email-triage-commands \
  -o jsonpath='{.spec.template.spec.containers[0].image}')}
pod="email-triage-search-$(date +%s)-$RANDOM"

cleanup() {
  kubectl -n "$namespace" delete pod "$pod" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# NUL-separated through stdin because jq would read a leading -- in an argument
# as one of its own options.
args_json=$(if [ "$#" -gt 0 ]; then printf '%s\0' "$@"; fi | jq -Rs 'split("\u0000")[:-1]')

jq -n --arg pod "$pod" --arg ns "$namespace" --arg image "$image" \
  --argjson args "$args_json" --argjson deadline "$deadline" '{
  apiVersion: "v1",
  kind: "Pod",
  metadata: {
    name: $pod,
    namespace: $ns,
    labels: {"app.kubernetes.io/name": "email-triage-bot", "app.kubernetes.io/component": "search"}
  },
  spec: {
    restartPolicy: "Never",
    activeDeadlineSeconds: $deadline,
    automountServiceAccountToken: false,
    securityContext: {
      runAsNonRoot: true,
      runAsUser: 65532,
      runAsGroup: 65532,
      seccompProfile: {type: "RuntimeDefault"}
    },
    containers: [{
      name: "search",
      image: $image,
      imagePullPolicy: "IfNotPresent",
      command: ["/email-triage-bot", "search"],
      args: $args,
      securityContext: {
        allowPrivilegeEscalation: false,
        capabilities: {drop: ["ALL"]},
        readOnlyRootFilesystem: true
      },
      envFrom: [
        {configMapRef: {name: "email-triage-archive"}},
        {secretRef: {name: "email-triage-archive"}}
      ],
      resources: {
        requests: {cpu: "10m", memory: "32Mi"},
        limits: {cpu: "500m", memory: "256Mi"}
      }
    }]
  }
}' | kubectl create -f - >/dev/null

# Waits for the pod to finish rather than using `kubectl run --rm`, which prints
# its own lines into stdout and would corrupt --json.
for ((waited = 0; waited <= deadline + 30; waited += 2)); do
  phase=$(kubectl -n "$namespace" get pod "$pod" -o jsonpath='{.status.phase}')
  case "$phase" in
    Succeeded | Failed) break ;;
  esac
  reason=$(kubectl -n "$namespace" get pod "$pod" -o jsonpath='{.status.containerStatuses[*].state.waiting.reason}')
  case "$reason" in
    ErrImagePull | ImagePullBackOff | InvalidImageName | CreateContainerConfigError)
      echo "search.sh: pod cannot start: $reason" >&2
      exit 1
      ;;
  esac
  sleep 2
done

case "$phase" in
  Succeeded | Failed) ;;
  *)
    echo "search.sh: pod did not finish (phase $phase)" >&2
    exit 1
    ;;
esac

kubectl -n "$namespace" logs "$pod"
code=$(kubectl -n "$namespace" get pod "$pod" -o jsonpath='{.status.containerStatuses[*].state.terminated.exitCode}')
exit "${code:-1}"
