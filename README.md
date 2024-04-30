# MySQL rpm (re)builder

## Overview

### Trigger repeatable MySQL rpm builds from a src rpm.

This repo has two main purposes:
- build binary rpms from the src.rpm in a repeatable manner ensuring the
  exact build requirements are defined.
  While rpm does have a `BuildRequires:` section intended to provide the
  list of build dependencies often this is far from precise and it is
  often incomplte.  This can mean that if the your build environment is
  setup differently to mine you can build binary packages but I can not,
  something which is problematic.  This repo intended to make the OS
  environment build process explict (e.g when building from a docker
  image) and also ensures the subsequent rpm-build run is explicit too.
- build patched versions of the original src rpm with minimal changes.
  along the same lines as the pervious step we can now build a patched
  version of the upstream src.rpms by providing the required changes,
  usually a patch, and building using the same process as before.

**Intention**: to build from an empty OS container, ensure the build
environment is setup explicitly and then to build as a non-root user.

I have found that the docs on performing a rebuild, at least as done
by upstream vendors may be far from complete and this had made building
for a new OS or wanting to build the existing software with a specific
patch much harder than expected.

Given a known initial state, the bare OS from docker 2 stages (**prepare**)
and (**rpm build**) are triggered by explicit scripts, and the whole
process is completely defined and should be repeatable.

A similar usage might happen when changing from one major OS version to
another or if changing from one major software version to another: all
changes becomes much more visible.

This is clearly work in progress. If you have feedback to provide you
can reach me at `sjmudd` at `pobox.com` or file an [issue](https://github.com/sjmudd/mysql-rpm-builder/issues/new)
on github directly.

## Directory Layout

- `build`             intended to build a single rpm, used from within docker
- `build_one`         intended to build a single rpm, used from outside docker
- `build_all`         intended to build all rpms, used from outside docker. Takes no parameters.
- `build.conf`        configuration file for `build` indicating which scripts
                      should be used for preparing the OS or building MySQL.
- `built/`            directory containing built rpms.
- `config/`           build configuration directory.
- `config/<VERSION>`  an optional directory of `SOURCES/` or `SPECS/` override
                      files when building the rpm. These files will be placed
                      in the appropriate directory after instaling the given
                      `.src.rpm` file, allowing build configuration to be
                      modified fromt he original src.rpm files.
- `SRPMS/`            cached or non-cached `SRPMS` files. If configured the
                      `SRPMS` may be downloaded here from an external site once
                      and reused later.
- `log/`              log files of completed or failed builds.

## Build Process

### Simple way

Typical usage would be:

- `build_one <os> <verson>`
- e.g. `build_one centos8 8.0.36`

If you don't provide either parameter you'll be prompted for valid values.

### What's under the hood?

`build_one` basically calls docker with the required parameters as shown:

```
$ docker run --rm -it \
        --network=host \
        --hostname=buildhost \
        -v $PWD:/data quay.io/centos/centos:stream8 \
        /data/build -a 8.0.33
```

Other examples might be:
```
$ docker run --rm -it --network=host --hostname=mysql-builder -v $PWD:/data oraclelinux:9 /data/build -a 8.0.36
```

However, if the process fails you won't have access to the state of the build
at the moment it fails. If you need to verify what breaks go through the
3 below steps individually.

Build failures are typically due to failure to ensure the required
repos are configured, allowing all the rpms to be found and installed.
This is troublesome as the rpm spec file does not say WHERE the rpms
come from. In the old days you'd expect the required rpms to be in the
base OS repos, but for MySQL builds we are using newer compilers than
the default system `gcc` and so the toolset and required rpms will be
in one of several external repos that need configuring.  It turns out
that repo naming and setup is different for each flavour of the OS. So
if you see a build failure such as:

```
...
Installed:
  dbus-libs-1:1.12.20-8.el9.x86_64
  dnf-plugins-core-4.3.0-13.el9.noarch
  python3-dateutil-1:2.8.1-7.el9.noarch
  python3-dbus-1.2.18-2.el9.x86_64
  python3-dnf-plugins-core-4.3.0-13.el9.noarch
  python3-six-1.15.0-9.el9.noarch
  python3-systemd-234-18.el9.x86_64
  systemd-libs-252-32.el9.x86_64

Complete!
### Enabling extra repo:
### installing required rpms
Last metadata expiration check: 0:00:24 ago on Wed Apr  3 05:32:32 2024.
No match for argument: libfido2-devel
No match for argument: libtirpc-devel
Error: Unable to find a match: libfido2-devel libtirpc-devel
```

this is most likely the cause and a bit of investigation is required to
find the rpms and setup the required repo accordingly.

### Building in individual steps

Alternatively it can be done in 3 steps as indicated below

#### Create docker container:

```
$ docker run --rm -it \
        --network=host \
        --hostname=rpm-builder \
        -v $PWD:/data \
        quay.io/centos/centos:stream8 \
	bash
```
or
```
$ ./start-docker-container.sh [<image_to_use>]
```

Current images are:
- AlmaLinux 8: almalinux:8
- AlmaLinux 9: almalinux:9
- CentOS 7: quay.io/centos:7
- CentOS 8 stream: quay.io/centos/centos:stream (default image)
- CentOS 9 stream: quay.io/centos/centos:stream9
- OEL 8: oraclelinux:8
- Rocky Linux 8: rocky:8
- Rocky Linux 9: rocky:9

#### Within docker container, as root run:

```
# sh /data/build 8.0.33 # setup os as required for this version
# su - rpmbuild         # change to rpmbuild build user
```

#### Without exiting the shell perform the build

```
# build 8.0.33 rpm from src.rpm configured in $SRPMS in the build script
# configured in /data/config/build.conf or cached copy in /data/SRPMS if
# present.
$ sh /data/build 8.0.33
```

### Output and Logging

If successful the final binary rpms should be found in
`/data/built/` under the OS / MySQL configuration name that had been
configured.

The build process will save logs in the `/data/log` directory, based on
the mysql version configuration specified and the OS found. Logging is
in UTC. The `/data/log` directory is actually kept as it's located within
the `$PWD` you build from. This allows for inspection of the build logs even
if you run the build completely from docker with `--rm` to remove the
created run image after completion of the build.

If successful the list of installed rpms required to perform the build
is also recorded as this may change over time or if the build fails it is
useful to share with others in case the installed rpms are not correct.

## A note on OS labels

The labels are NOT random.  They come from `/etc/os-release`:

and the 2 values:
```
ID="rocky"
VERSION_ID="9.3"
```

With the first major version number being used.

e.g. this resolves to `rocky9`.

## Patching

If you want to patch any of the SRPMS this can be done by doing the
following:
- create a new version directory under `/data/config` for the special build
- update `/data/config/build.conf` to refer to that version and the required
  prepare or build scripts if they need to change.
- add any required files to be placed in the `~/rpmbuild/SOURCES` or
  `~/rpmbuild/SPECS` directories under `/data/config/<version>/SOURCES` or
  `/data/config/<version>/SPECS` directories as these will automatically
  be added after installing the `src.rpm`.

In practice you may want or need to patch the mysql.spec file so you'd have a file
like /data/config/<version>/SPECS/mysql.spec.patch with the appropriate
patch against files in the directory so the beginning of the diff might look like

```
--- mysql.spec.orig     2023-11-02 21:20:49.863472158 +0100
+++ mysql.spec  2023-11-02 21:29:35.143983290 +0100
... patch goes here ...
```

With patches against the source tree the patch would be located here:

`config/<version>/SOURCES/patch_name.diff`

and the patch contents will look something like:

```
diff --git a/CMakeLists.txt b/CMakeLists.txt
index 5f4cc06f30c..31d63ba40f6 100644
--- a/mysql-8.2.0/CMakeLists.txt
+++ b/mysql-8.2.0/CMakeLists.txt
... patch goes here ...

```

with the change in the mysql.spec file being something like:

```
--- mysql.spec.orig     2023-11-02 21:20:49.863472158 +0100
+++ mysql.spec  2023-11-02 21:29:35.143983290 +0100
@@ -150,7 +150,7 @@
 Summary:        A very fast and reliable SQL database server
 Group:          Applications/Databases
 Version:        8.2.0
-Release:        1%{?commercial:.1}%{?dist}
+Release:        1%{?commercial:.1}%{?dist}.hypergraph
 License:        Copyright (c) 2000, 2023, %{mysql_vendor}. Under %{?license_type} license as shown in the Description field.
 Source0:        https://cdn.mysql.com/Downloads/MySQL-8.2/%{src_dir}.tar.gz
 URL:            http://www.mysql.com/
@@ -162,6 +162,7 @@
 Source10:       https://boostorg.jfrog.io/artifactory/main/release/1.77.0/source/boost_1_77_0.tar.bz2
 Source90:       filter-provides.sh
 Source91:       filter-requires.sh
+Patch0:         000.hypergraph_optimizer_enable.diff
 %if 0%{?rhel} >= 8
 BuildRequires:  cmake >= 3.6.1
 BuildRequires:  libtirpc-devel
@@ -792,6 +793,8 @@
 %else
 %setup -q -T -a 0 -a 10 -c -n %{src_dir}
 %endif # 0%{?compatlib}
+# 000 Enable hypergraph optimizer
+%patch0 -p1

 %build
 # Fail quickly and obviously if user tries to build as root
```

## Warning on differences between different equivalent OS versions.

I tend to use [CentOS](centos.org) as the Linux flavour of
interest. This is the unlicensed, fully GPL version of [RedHat Enterprise Linux](https://www.redhat.com/en/technologies/linux-platforms/enterprise-linux).
There are other similar flavours which were released after the unexpected
termination of CentOS 8 and its replacement with CentOS 8 Stream.
The intention of all these repos is to be equivalent, but in fact there
are some differences and what may work on one OS may not work on the
others at least without some minimal changes.

I have seen some differences between OL8 and CentOS 8 Stream with the
names of additional repos used and it looks like CentOS 9 may have
other differences with its _brothers_.  Most of this is easy to fix,
but none of it is explicit, requiring you to make unexpected changes
prior to being able to rebuild MySQL rpms.

## Build times

I was somewhat suprised at how long it takes to rebuild the rpms.
I normally do this very rarely.  I have also done this from the git
source tree provided by Oracle.

The rpm rebuild times seem to be quite a long longer. This is I believe
because both the normal and debug builds take place increasing the build
times. On a home system I have (Beelink SER 4700u) this takes about 2h
45m using a NAS vs 1h 20m using local nvme storage.  The C/C++ build
process reads and writes a lot to the filesystem so storage latency can
be signifcant.

NOTE:

RPM builds by Oracle run the build process twice, once for the normal
builds and once to create debug builds which provide full symbols etc.
This means that build times are longer than you might otherwise expect
as the debug builds contain a lot of extra debug / symbol information
all of which is built into the debug rpms.

## Related thoughts.

None of what is done here is specific to MySQL so these scripts could
be used for building other packages following the same philosophy.

Others may ask why I build from the src.rpm files and not directly from
the git repo (in the case of MySQL). That might be an interesting addition
to the tooling as in the same way that explicit documentation on the build
process from src.rpms to binary rpms is often missing the same explict
instructions for triggering repeatable rpm builds from the git tree may
be applicable. [Here](https://github.com/sjmudd/bacula-rpm-builder/) is
an example of that.   As the git trees of many packages are the ultimate
source using those is clearly better.

As of April 2024 The OS7 / OS8 versions are close to EOL so moving to
OS9 will be needed.  I guess none of the OS "vendors" will provide
sort for newer versions of MySQL on these versions. Perhaps the
rebuilds will allow that to be possible.

MySQL 8.4 is due out very shortly. I'll endevour to adjust this rebuild
process to work with the new versions. Most likely that since the 8.3.0
builds work 8.4.X should rebuild in a similar way.
