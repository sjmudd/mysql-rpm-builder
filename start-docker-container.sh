#!/bin/sh

# default image:
# - quay.io/centos/centos:stream8
#
# other images:
# - quay.io/centos/centos:stream9
# - almalinux:8.7
# - oraclelinux:8.7
# - oraclelinux:9
# - rockylinux:8.7
#
# To build and install in one go do the following:
# docker run --rm -it --network=host --hostname=mysql-builder -v $PWD:/data rockylinux:8.7 /data/build -a 8.0.33
#
myname=$(basename $0)
default_image=quay.io/centos/centos:stream8

usage () {
	local rc=${1:-0}

	cat <<-EOF | sed -e 's/^#//'
	#$usage (C) 2023-2024 Simon J Mudd <sjmudd@pobox.com>
	#Script to start docker and build MySQL rpms with given parameters
	#
	#Usage: $myname [-h][-i <image>] command...
	#
	#-h provide this help messsage
	#-i <image> provide the docker image to run. Default is $image.
	#
	#A typical command will be something like '/data/build -a 8.3.0'
	#
	#Possible images:
	#- quay.io/centos/centos:7
	#- quay.io/centos/centos:stream8 (default)
	#- quay.io/centos/centos:stream9
	#- almalinux:8.7
	#- oraclelinux:8.7
	#- oraclelinux:9
	#- rockylinux:8.7
	EOF

	exit $rc
}

image=$default_image
while getopts ih flag; do
	case $flag in
	h)	usage 0;;
	i)	image=$OPTARG;;
	*)	usage 1;;
	esac
done
shift $(($OPTIND - 1))

echo "Starting mysql-rpm-builder using image: $image and parameters $*"
docker run --rm -it --network=host --hostname=mysql-builder -v $PWD:/data $image $*
