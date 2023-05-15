#!/bin/sh

docker run --rm -it --network=host --hostname=mysql-builder -v /data1/src/work:/data quay.io/centos/centos:stream8
