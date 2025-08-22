#!/bin/sh

############################################################################
#                                                                          #
# OS Setup for OL10 for 9.4.0                                              #
#                                                                          #
############################################################################

set -e

cd /data

yum update -y
yum install -y 'dnf-command(config-manager)'

# OEL10 differences vs CentOS 10 stream
if rpm -q centos-stream-release 2>&1 >/dev/null; then
	echo "- found CentOS 10 stream"
	extra_repo=crb
elif rpm -q oraclelinux-release 2>&1 >/dev/null; then
	echo "- found Oracle Linux 10"
	extra_repo=ol10_codeready_builder
elif rpm -q almalinux-release 2>&1 >/dev/null; then
	echo "- found Alma Linux 10"
	extra_repo=crb
elif rpm -q rocky-release 2>&1 >/dev/null; then
	echo "- found Rocky Linux 10"
	extra_repo=crb
else
	echo "- OS not recognised, giving up"
	exit 1
fi
echo "### Enabling extra repo: $extra_repo"
yum config-manager --set-enabled $extra_repo

echo "### installing required rpms"
yum install -y \
	annobin-annocheck \
	annobin-plugin-gcc \
	bind-utils \
	binutils  \
	bison \
	cmake \
	cyrus-sasl-devel \
	dwz \
	gcc \
	gcc-c++ \
	gcc-plugin-annobin \
	git \
	krb5-devel \
	libaio-devel \
	libcurl-devel \
	libfido2-devel \
	libquadmath-devel \
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
	wget \
	zlib-devel

# temporarily remove the arch gcc-toolset plugindir patching

echo "########################################################"
echo "#           os preparation complete                    #"
echo "########################################################"
