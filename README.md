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

### Build in one go

The **whole process** can now be done with a single command from the docker server:

```
$ docker run --rm -it \
        --network=host \
        --hostname=rpm-builder \
        -v $PWD:/data quay.io/centos/centos:stream8 \
        /data/build -a 8.0.33
```

However, if the process fails you won't have access to the state of the build
at the moment it fails. If you need to verify what breaks go through the
3 below steps individually.

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
- AlmaLinux 8: almalinux:8.7
- CentOS 8 stream: quay.io/centos/centos:stream (default)
- OEL 8: oraclelinux:8.7
- Rocky Linux 8: rocky:8.7

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
in UTC.

If successful the list of installed rpms required to perform the build
is also recorded as this may change over time or if the build fails it is
useful to share with others in case the installed rpms are not correct.

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

## Warning on differences between different equivalent OS versions.

I tend to use [CentOS](centos.org) as the Linux flavour of interest. This is the
unlicensed, fully GPL version of [RedHat Enterprise Linux](https://www.redhat.com/en/technologies/linux-platforms/enterprise-linux).
There are other similar flavours which were released after the unexpected termination of
CentOS 8 and its replacement with CentOS 8 Stream.  The intention of all
these repos is to be equivalent, but in fact there are some differences
and what may work on one OS may not work on the others at least without
some minimal changes.

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
because both the normal and debug builds take place making the process
much longer. On a home system I have (SER 4700u) this takes about 2-3
hours.  The C/C++ build process reads and writes a lot to the filesystem
so storage latency may be signifcant.

## Related thoughts.

None of what is done here is specific to MySQL apart and these scripts
could be used for building other packages following the same philosophy.

Others may ask why I build from the src.rpm files and not directly
from the git repo (in the case of MySQL). That might be an interesting
addition to the tooling as in the same way that explicit documentation
on the build process from src.rpms to binary rpms is often missign the
same explict instructions for triggering repeatable builds from the
git tree may be applicable. As the git trees of many packages are the
ultimate source using those is clearly better.

## TODO

I should probably provide a mechanism for patching the `mysql.spec`
file rather than replacing it completely as usually patches for a
some minor change and this is more explicit than modifying the whole
`mysql.spec` file.  This would also make it much easier to apply the same
patch unmodified to different versions.  This change has not been done
yet but may be added later.
