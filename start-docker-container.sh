#!/bin/sh

# default image:
# - quay.io/centos/centos:stream8
#
# other images:
# - almalinux:8.7
# - oraclelinux:8.7
# - rockylinux:8.7
#
# To build and install in one go do the following:
# docker run --rm -it --network=host --hostname=mysql-builder -v $PWD:/data rockylinux:8.7 /data/build -a 8.0.33
#
image=${1:-quay.io/centos/centos:stream8}
shift

echo "Starting mysql-rpm-builder using image: $image and parameters $*"
docker run --rm -it --network=host --hostname=mysql-builder -v $PWD:/data $image $*
