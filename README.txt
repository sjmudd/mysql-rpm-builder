############################################################################
#                                                                          #
#                         mysql rpm rebuilder                              #
#                                                                          #
# Trigger repeatable rpm builds from src rpm, but making it easy to apply  #
# patches to upstream sources.                                             #
#                                                                          #
# Intention: to build from an empty OS container, ensure the build         #
# environment is setup explicitly and then to build as a non-root user.    #
#                                                                          #
# I found the current build process is not very strictly defined at least  #
# the BuildRequires entries in the mysql.spec file seems incomplete.       #
# This repo is intended to make it easier to rebuild from provided sources #
# and at the same time to record the specific build process more           #
# more explicitly.                                                         #
#                                                                          #
# This is clearly work in progress. If you have feedback to provide you    #
# can reach me at sjmudd at pobox.com or file an issue on github directly. #
#                                                                          #
############################################################################

Directory layout:
- config/<VERSION> has per version configuration and build scripts, possibly
  including any local patches that might need to be applied.
- rpmbuild/ directory is for building rpms for the non-root build user
- SRPMS/ contains cached or non-cached SRPMS files. If configured the SRPMS
  may be downloaded here from an external site once and reused later.

Build process:
(1) Create docker container:
    $ docker run --rm -it --network=host --hostname=builder -v $PWD:/data quay.io/centos/centos:stream

(2) Within docker container, as root run:
    # sh /data/build-environment.sh 8.0.32 # setup os as required
    # su - rpmbuild                        # change to rpmbuild build user

(3) Without exiting the shell perform the build
    # build 8.0.32 rpm from src.rpm configured in $SRPMS in
    # /data/config/8.0.32/build.sh or cached copy in /data/SRPMS if present.
    $ sh build-environment.sh 8.0.32

If successful the final binary rpms should be found in
~/rpmbuild/RPMS/<arch> and final src rpm should be found in
~/rpmbuild/SRPMS/.

The build process will save logs in the ~/rpmbuild/SPECS directory.

If successful the list of installed rpms is also recorded. This may
change over time.
