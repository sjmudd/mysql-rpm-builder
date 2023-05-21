#!/bin/sh

set -e

prepare() {
	cd /data

	yum update -y
	yum install -y 'dnf-command(config-manager)'

	# OEL9 differences vs CentOS 9 stream ???
	if  rpm -qa | grep -q centos-stream-release-9; then
		extra_repo=crb
	elif rpm -q oraclelinux-release 2>&1 >/dev/null; then
		extra_repo=ol9_codeready_builder
	fi
	echo "### Enabling extra repo: $extra_repo"
	yum config-manager --set-enabled $extra_repo

	echo "### installing required rpms"
	yum install -y \
		bind-utils \
		bison \
		cmake \
		cyrus-sasl-devel libaio-devel \
		git \
		libcurl-devel \
		libtirpc-devel \
		libudev-devel \
		ncurses-devel \
		numactl-devel \
		openldap-devel \
		openssl-devel \
		perl \
		perl-JSON rpcgen \
		rpm-build \
		time \
		gcc-toolset-12-annobin-annocheck \
		gcc-toolset-12-annobin-plugin-gcc \
		gcc-toolset-12-binutils  \
		gcc-toolset-12-gcc \
		gcc-toolset-12-gcc-c++ \
		wget \
		zlib-devel

	echo "########################################################"
	echo "#           os preparation complete                    #"
	echo "########################################################"
}
