#!/usr/bin/env bash
set -e

cd ${GOPATH}/src/github.com/allanhung/flexvolume/
GIT_SHA=`git rev-parse --short HEAD || echo "HEAD"`


export GOARCH="amd64"
export GOOS="linux"
if [[ "$(uname -s)" == "Linux" ]];then
	CGO_ENABLED=1 go build -tags 'netgo' --ldflags '-extldflags "-static"' -o flexvolume-subpath-linux 
else
	CGO_ENABLED=0 go build -o flexvolume-subpath-linux 
fi

