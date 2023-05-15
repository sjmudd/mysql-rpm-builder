#!/bin/sh

# other images:
# - quay.io/centos/centos:stream8 (DEFAULT)
# - oraclelinux:8.7
image=${1:-quay.io/centos/centos:stream8}

echo "Starting mysql-rpm-builder using image: $image"
docker run --rm -it --network=host --hostname=mysql-builder -v $PWD:/data $image
