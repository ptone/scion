# syntax=docker/dockerfile:1
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

ARG BASE_IMAGE
FROM ${BASE_IMAGE}

USER root

RUN mkdir -p /home/scion/.gemini/antigravity-cli \
  && mkdir -p /home/scion/.agents \
  && chown -R scion:scion /home/scion/.gemini /home/scion/.agents

# Install Antigravity CLI via official install script
RUN curl -fsSL https://storage.googleapis.com/antigravity-public/antigravity-cli/tools/install.sh | bash -s -- --dir /usr/local/bin

CMD ["agy"]
