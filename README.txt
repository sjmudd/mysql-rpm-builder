############################################################################
#                                                                          #
#                         mysql rpm (re)builder                            #
#                                                                          #
# Trigger repeatable rpm builds from a src rpm.                            #
#                                                                          #
# This has two main purposes:                                              #
# - build binary rpms from the src.rpm in a repeatable manner ensuring the #
#   exact build requirements are defined.                                  #
#   While rpm does have a BuildRequires section intended to provide the    #
#   list of build dependencies often this is far from precise and it is    #
#   often incomplte.  This can mean that if the your build environment is  #
#   setup differently to mine you can build binary packages but I can not, #
#   something which is problematic.  This repo intended to make the OS     #
#   environment build process explict (e.g when building from a docker     #
#   image) and also ensures the subsequent rpm-build run is explicit too.  #
# - build patched versions of the original src rpm with minimal changes.   #
#   along the same lines as the pervious step we can now build a patched   #
#   version of the upstream src.rpms by providing the required changes,    #
#   usually a patch, and building using the same process as before.        #
#                                                                          #
# Intention: to build from an empty OS container, ensure the build         #
# environment is setup explicitly and then to build as a non-root user.    #
#                                                                          #
# I have found that the docs on performing a rebuild, at least as done     #
# by upstream vendors may be far from complete and this had made building  #
# for a new OS or wanting to build the existing software with a specific   #
# patch much harder than expected.                                         #
#                                                                          #
# Given the OS (prepare.sh) and rpm build stages are triggered by explicit #
# scripts, starting from a known initial state (the base docker image)     #
# the whole process is completely defined and should be repeatable.        #
#                                                                          #
# A similar usage might happen when chaning from one major OS version to   #
# another or if changing from one major software version to another: all   #
# changes becomes much more visible.                                       #
#                                                                          #
# This is clearly work in progress. If you have feedback to provide you    #
# can reach me at sjmudd at pobox.com or file an issue on github directly. #
#                                                                          #
############################################################################

Directory layout:
- config/            build configuration directory.
- config/build.conf  configuration file indicating which scripts should be
                     used for preparing the OS or building MySQL
- config/<VERSION>   an optional directory of SOURCES/ or SPECS/ override
                     files when building the rpm. These files will be placed
                     in the appropriate directory after instaling the given
                     .src.rpm file, allowing build configuration to be
                     modified fromt he original src.rpm files.
- rpmbuild/          directory is for building rpms for the non-root build
                     user.
- SRPMS/             cached or non-cached SRPMS files. If configured the
                     SRPMS may be downloaded here from an external site once
                     and reused later.
- log/               log files of completed or failed builds.

Build process:
(1) Create docker container:
    $ docker run --rm -it --network=host --hostname=builder -v $PWD:/data quay.io/centos/centos:stream8
        or
    $ ./start-docker-container.sh [<image_to_use>]

    Current images are:
    - AlmaLinux 8: almalinux:8.7
    - CentOS 8 stream: quay.io/centos/centos:stream (default)
    - OEL 8: oraclelinux:8.7
    - Rocky Linux 8: rocky:8.7

(2) Within docker container, as root run:
    # sh /data/build-environment.sh 8.0.33 # setup os as required for this version
    # su - rpmbuild                        # change to rpmbuild build user

(3) Without exiting the shell perform the build
    # build 8.0.33 rpm from src.rpm configured in $SRPMS in the build script
    # configured in /data/config/build.conf or cached copy in /data/SRPMS if
    # present.
    $ sh build-environment.sh 8.0.33

If successful the final binary rpms should be found in
~/rpmbuild/RPMS/<arch> and final src rpm should be found in
~/rpmbuild/SRPMS/.

The build process will save logs in the ~/log directory, based on the
build_environment name and build start time in UTC.

If successful the list of installed rpms required to peform the build
is also recorded as this may change over time or if the build fails it is
useful to share with others in case the installed rpms are not correct.

If you want to patch any of the SRPMS this can be done by doing the
following:
- create a new version directory under /data/config for the special build
- update /data/config/build.conf to refer to that version and the required
  prepare or build scripts if they need to change.
- add any required files to be placed in the ~/rpmbuild/SOURCES or
  ~/rpmbuild/SPECS directories under /data/config/<version>/SOURCES or
  /data/config/<version>/SPECS directories as these will automatically
  be added after installing the src.rpm.

FIXME: I should probably provide a mechanism for patching the mysql.spec
file rather than replacing it completely as usually patches for a
some minor change and this is more explicit than modifying the whole
mysql.spec file.  This would also make it much easier to apply the same
patch unmodified to different versions.  This change has not been done
yet but may be added later.
