#!/bin/bash
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/. */
#
# This script file attempts to build a new gpm compatible Godeps file
# from the current project list of go libraries. It works on my system,
# it may not work on yours, but I'm happy to get patch updates.
#
# this is loosely based on gpm-boostrap
# https://github.com/pote/gpm-bootstrap
get_ver(){
    ver=''
    if [ -d .hg ]; then
        echo mercurial
        ver=`hg log | head -1 | sed "s/changeset:\s*//"`
    elif [ -d .git ]; then
        echo git
        ver=`git log |head -1 | sed "s/commit //"`
    fi
}
#Main...
declare -a list=()
wd=`pwd`
echo Fetching libraries...
libs=`GOPATH=$wd/.godeps:$wd go list -f '{{join .Deps "\n"}}' ./... |xargs go list -f '{{if not .Standard}}{{.ImportPath}}{{end}}'`
echo Getting versions...
for lib in $libs ; do
    dir=$wd/.godeps/src/$lib
    if [ -d $dir ]; then
        cd $dir
        echo -n ">> $dir -- "
        get_ver
        if [ "$ver" == "" ]; then
            # In some cases, the version is in the parent.
            # Probably should not go beyond that.
            cd ..
            get_ver
            if [ "$ver" == "" ]; then
                echo Unknown
                continue
            fi
        fi
        list+=("$lib $ver|")
    else
        echo Ignoring $wd/.godeps/src/$lib
    fi
done
cd $wd
echo Adding to Godeps.new
echo ${list[@]} | sed "s/|/\n/g" | sed "s/^\s*//" >> Godeps.new
echo done!
