#!/bin/sh

docker run --rm -it --network=host --hostname=mysql-builder -v $PWD:/data quay.io/centos/centos:stream8
