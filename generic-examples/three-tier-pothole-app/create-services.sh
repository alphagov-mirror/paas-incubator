#!/usr/bin/env bash

set -eu

# This script should be run once when the space is set up to create the persistent services.

cf create-service postgres tiny-unencrypted-10 backend-postgres
cf create-service aws-s3-bucket default three-tier-pothole-imgs -c '{"public_bucket":true}'
