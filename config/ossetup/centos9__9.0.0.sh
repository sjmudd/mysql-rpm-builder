############################################################################
#                                                                          #
# OS Setup functions for OS9                                               #
#                                                                          #
############################################################################

set -e

prepare() {
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

	echo "### installing required rpms"
	yum install -y \
		bind-utils \
		bison \
		cmake \
		cyrus-sasl-devel \
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
		perl \
		perl-JSON rpcgen \
		rpm-build \
		time \
		gcc-toolset-12-annobin-annocheck \
		gcc-toolset-12-annobin-plugin-gcc \
		gcc-toolset-12-binutils \
		gcc-toolset-12-gcc \
		gcc-toolset-12-gcc-c++ \
		gcc-toolset-13-annobin-annocheck \
		gcc-toolset-13-annobin-plugin-gcc \
		gcc-toolset-13-binutils  \
		gcc-toolset-13-dwz \
		gcc-toolset-13-gcc \
		gcc-toolset-13-gcc-c++ \
		wget \
		zlib-devel

	# FIXME: Handle aarch64 architecture.
	# See: https://bugs.mysql.com/bug.php?id=108049. Without this symlink the
	# build process will fail to find something the code needs to complete
	# the build.
	#
	# Even better in 9.X we have to support this symlinking in CentOS9 for
	# both gcctoolset-12 (8.0/8.4 compat builds) and gcctoolset-13 (9.0 builds)
	gcctoolset_versions="12 13"
	for version in $gcctoolset_versions; do
		PLUGINDIR=/opt/rh/gcc-toolset-$version/root/usr/lib/gcc/x86_64-redhat-linux/$version/plugin
		pushd $PLUGINDIR

		echo "Handling CentOS 9 workaround symlinks for gcc-toolset-$version"
		for p in annobin.so annobin.so.0.0.0 gcc-annobin.so gcc-annobin.so.0.0.0; do
			echo "Symlinking missing $p..."
			test -e $p || ln -s gts-annobin.so.0.0.0 $p
		done
		popd
	done

	echo "########################################################"
	echo "#           os preparation complete                    #"
	echo "########################################################"
}
