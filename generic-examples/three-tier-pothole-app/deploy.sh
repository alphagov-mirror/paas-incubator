#!/usr/bin/env bash

set -eu

(
  cd backend
  cf push
)

(
  cd frontend
  cf push
)

(
  cf add-network-policy three-tier-frontend --destination-app three-tier-backend
)
