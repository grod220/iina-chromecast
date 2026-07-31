#!/bin/bash
# one arch each, then stitch into a universal binary
GOOS=darwin GOARCH=arm64 go build -o casthelper-arm64 .
GOOS=darwin GOARCH=amd64 go build -o casthelper-amd64 .
lipo -create -output casthelper casthelper-arm64 casthelper-amd64

# ad-hoc sign is enough when the plugin is installed via "Install from GitHub"
# (git-cloned files aren't quarantined). Use Developer ID + notarization only if
# you want it robust against every install path.
codesign -s - --options runtime casthelper
chmod +x casthelper
mv casthelper ../
rm casthelper-arm64
rm casthelper-amd64