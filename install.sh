#!/bin/bash
set -e

echo "Installing DoToken..."

# Download latest release
curl -sL https://github.com/ssupawat/dotoken/releases/latest/download/dotoken-darwin.zip -o /tmp/dotoken.zip

# Install .app bundle to Applications
if [ -d /Applications/DoToken.app ]; then
  rm -rf /Applications/DoToken.app
fi
unzip -o /tmp/dotoken.zip -d /tmp/dotoken-install
mv /tmp/dotoken-install/DoToken.app /Applications/
rm -rf /tmp/dotoken.zip /tmp/dotoken-install

echo "DoToken installed to /Applications/DoToken.app"
echo "Open it from Applications. To start it automatically, tick \"Open DoToken at login\" in its settings."
