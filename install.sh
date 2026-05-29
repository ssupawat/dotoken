#!/bin/bash
set -e

mkdir -p ~/bin
curl -sL https://github.com/ssupawat/dotoken/releases/latest/download/dotoken-darwin.zip -o /tmp/dotoken.zip
unzip -o /tmp/dotoken.zip -d ~/bin
rm /tmp/dotoken.zip
grep -q '\$HOME/bin' ~/.zshrc 2>/dev/null || echo 'export PATH="$HOME/bin:$PATH"' >> ~/.zshrc
echo "DoToken installed to ~/bin/dotoken"
echo "Run 'dotoken' to start, or add it to System Settings → General → Login Items for auto-start."
