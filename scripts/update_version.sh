#!/usr/bin/env bash

# Calculate version and commit
VERSION="0.1.$(git rev-list --count HEAD)"
COMMIT=$(git rev-parse --short HEAD)
DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

FILE="internal/app/version.go"

# Use sed to update the values in place
# Note: This assumes the format in version.go is exactly as seen in Read tool
sed -i '' "s/Version = \".*\"/Version = \"$VERSION\"/" "$FILE"
sed -i '' "s/Commit  = \".*\"/Commit  = \"$COMMIT\"/" "$FILE"
sed -i '' "s/Date    = \".*\"/Date    = \"$DATE\"/" "$FILE"

echo "Updated $FILE to Version: $VERSION, Commit: $COMMIT, Date: $DATE"
