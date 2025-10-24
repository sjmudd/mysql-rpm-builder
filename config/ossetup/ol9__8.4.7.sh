#!/bin/sh

############################################################################
#                                                                          #
# OS Setup functions for OS9 / 8.4                                         #
#                                                                          #
############################################################################

set -e

cd /data

yum update -y
yum install -y 'dnf-command(config-manager)'

# OEL9 differences vs CentOS 9 stream
if rpm -q centos-stream-release 2>&1 >/dev/null; then
	echo "- found CentOS 9 stream"
	extra_repo=crb
elif rpm -q oraclelinux-release 2>&1 >/dev/null; then
	echo "- found Oracle Linux 9"
	extra_repo=ol9_codeready_builder
elif rpm -q almalinux-release 2>&1 >/dev/null; then
	echo "- found Alma Linux 9"
	extra_repo=crb
elif rpm -q rocky-release 2>&1 >/dev/null; then
	echo "- found Rocky Linux 9"
	extra_repo=crb
else
	echo "- OS not recognised, giving up"
	exit 1
fi
echo "### Enabling extra repo: $extra_repo"
yum config-manager --set-enabled $extra_repo

# Install EPEL repo
if rpm -q oraclelinux-release 2>&1 >/dev/null; then
	echo "- setting up oracle-epel-release-el9 repo"
	yum -y install oracle-epel-release-el9
	yum config-manager --set-enabled ol9_developer_EPEL
elif rpm -q almalinux-release 2>&1 >/dev/null; then
	echo "- setting up epel-release repo"
	dnf install -y epel-release
elif rpm -q rocky-release 2>&1 >/dev/null; then
	echo "- setting up epel-release repo"
	dnf install -y epel-release
else
	echo "- EPEL repo handling not supported on this OS yet. Please fix"
	exit 1
fi

echo "### installing required rpms"
yum install -y \
	bind-utils \
	bison \
	cmake \
	cyrus-sasl-devel \
	gcc-toolset-12-annobin-annocheck \
	gcc-toolset-12-annobin-plugin-gcc \
	gcc-toolset-12-binutils  \
	gcc-toolset-12-dwz \
	gcc-toolset-12-gcc \
	gcc-toolset-12-gcc-c++ \
	git \
	krb5-devel \
	libaio-devel \
	libcurl-devel \
	libfido2-devel \
	libtirpc-devel \
	libudev-devel \
	ncurses-devel \
	numactl-devel \
	openldap-devel \
	openssl-devel \
	patchelf \
	perl \
	perl-JSON \
       	rpcgen \
	rpm-build \
	time \
	wget \
	zlib-devel

# FIXME: Handle aarch64 architecture.
# See: https://bugs.mysql.com/bug.php?id=108049. Without this symlink the
# build process will fail to find something the code needs to complete
# the build.
#
# Even better in 9.X we have to support this symlinking in CentOS9 for
# both gcctoolset-12 (8.0/8.4 compat builds) and gcctoolset-13/14 (9.0 builds)
ARCH=$(uname -m) # to handle x86_64 and aarch64
gcctoolset_versions="12 13 14"
for version in $gcctoolset_versions; do
	PLUGINDIR=/opt/rh/gcc-toolset-$version/root/usr/lib/gcc/$ARCH-redhat-linux/$version/plugin
	if [ -d $PLUGINDIR ]; then
		pushd $PLUGINDIR

		echo "Handling CentOS 9 workaround symlinks for gcc-toolset-$version"
		for p in annobin.so annobin.so.0.0.0 gcc-annobin.so gcc-annobin.so.0.0.0; do
			echo "Symlinking missing $p..."
			test -e $p || ln -s gts-annobin.so.0.0.0 $p
		done
		popd
	fi
done

echo "########################################################"
echo "#           os preparation complete                    #"
echo "########################################################"
