#!/usr/bin/env bash
# Build + deploy peersh-signaling to Google Cloud Run.
#
# Prerequisites:
#   - gcloud CLI installed + authenticated (`gcloud auth login`)
#   - The target GCP project has billing enabled
#
# Usage:
#   PROJECT_ID=my-project REGION=asia-northeast1 \
#     server/deploy/cloud-run/deploy.sh
#
# Or set the env vars first and run with no args. Both PROJECT_ID and
# REGION default to whatever `gcloud config` is currently set to.
#
# After the first deploy, set PEERSH_SIGNALING_DISCOVERY_WS_URL +
# PEERSH_SIGNALING_BOOTSTRAP_PSK on the service via:
#   gcloud run services update peersh-signaling \
#     --region=$REGION \
#     --update-env-vars=PEERSH_SIGNALING_DISCOVERY_WS_URL=wss://<host>/ws,\
# PEERSH_SIGNALING_BOOTSTRAP_PSK=alice:<hex>

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null || true)}"
REGION="${REGION:-asia-northeast1}"
REPO="${REPO:-peersh}"
SERVICE="${SERVICE:-peersh-signaling}"
TAG="${TAG:-latest}"

if [[ -z "$PROJECT_ID" ]]; then
  echo "ERROR: set PROJECT_ID (env var or via 'gcloud config set project')" >&2
  exit 1
fi

echo ">> project=$PROJECT_ID region=$REGION repo=$REPO service=$SERVICE"

# Make sure required APIs are enabled. Idempotent.
echo ">> enabling required APIs"
gcloud services enable \
  run.googleapis.com \
  cloudbuild.googleapis.com \
  artifactregistry.googleapis.com \
  --project="$PROJECT_ID"

# Create the Artifact Registry repo if it does not already exist.
if ! gcloud artifacts repositories describe "$REPO" \
      --location="$REGION" --project="$PROJECT_ID" >/dev/null 2>&1; then
  echo ">> creating Artifact Registry repo $REPO in $REGION"
  gcloud artifacts repositories create "$REPO" \
    --repository-format=docker \
    --location="$REGION" \
    --description="peersh container images" \
    --project="$PROJECT_ID"
fi

IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPO}/${SERVICE}:${TAG}"

# Submit the build. Cloud Build runs the Dockerfile and pushes both
# `:latest` and `:$BUILD_ID` tags to Artifact Registry.
echo ">> submitting build (image: $IMAGE)"
gcloud builds submit \
  --config=server/deploy/cloud-run/cloudbuild.yaml \
  --substitutions=_REGION="$REGION",_REPO="$REPO",_IMAGE="$SERVICE",_TAG="$TAG" \
  --project="$PROJECT_ID" \
  .

# Deploy to Cloud Run. --allow-unauthenticated lets the WebSocket
# endpoint be reachable from any client; auth is at the application layer
# (PSK / Firebase ID token).
echo ">> deploying to Cloud Run service $SERVICE"
gcloud run deploy "$SERVICE" \
  --image="$IMAGE" \
  --region="$REGION" \
  --platform=managed \
  --allow-unauthenticated \
  --port=8443 \
  --memory=512Mi \
  --cpu=1 \
  --min-instances=0 \
  --max-instances=2 \
  --concurrency=80 \
  --timeout=3600 \
  --set-env-vars=PEERSH_SIGNALING_LOG_LEVEL=info \
  --project="$PROJECT_ID"

URL=$(gcloud run services describe "$SERVICE" \
  --region="$REGION" --project="$PROJECT_ID" \
  --format='value(status.url)')

echo
echo "================================================================"
echo " Deployed."
echo " URL:   $URL"
echo " WS:    ${URL/https:\/\//wss:\/\/}/ws"
echo " Health $URL/healthz"
echo " Disco  $URL/.well-known/peersh.json"
echo "================================================================"
echo
echo "Next: set the discovery URL + a bootstrap PSK and redeploy:"
echo
echo "  gcloud run services update $SERVICE --region=$REGION \\"
echo "    --update-env-vars=PEERSH_SIGNALING_DISCOVERY_WS_URL=${URL/https:\/\//wss:\/\/}/ws,\\"
echo "    PEERSH_SIGNALING_BOOTSTRAP_PSK=alice:<your-hex-secret>:alice-laptop"
echo
