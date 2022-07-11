FROM ubuntu

ARG TARGETVERSION=v0.15.1
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# add a non-root 'steampipe' user
RUN adduser --system --disabled-login --ingroup 0 --gecos "steampipe user" --shell /bin/bash --uid 9193 steampipe

# updates and installs - 'wget' for downloading steampipe, 'less' for paging in 'steampipe query' interactive mode
RUN apt-get update -y && apt-get install -y wget less jq

# download the release as given in TARGETVERSION, TARGETOS and TARGETARCH
RUN echo \
    && cd /tmp \
    && wget -nv https://github.com/turbot/steampipe/releases/download/${TARGETVERSION}/steampipe_${TARGETOS}_${TARGETARCH}.tar.gz \
    && tar xzf steampipe_${TARGETOS}_${TARGETARCH}.tar.gz \
    && mv steampipe /usr/local/bin/ \
    && rm -rf /tmp/steampipe_${TARGETOS}_${TARGETARCH}.tar.gz

# Change user to non-root
USER steampipe:0

# disable auto-update
ENV STEAMPIPE_UPDATE_CHECK=false

# disable telemetry
ENV STEAMPIPE_TELEMETRY=none

# Run steampipe service once
RUN steampipe service start --dashboard
# and stop it
RUN steampipe service stop

# remove the generated service .passwd file from this image, so that it gets regenerated in the container
RUN rm -f /home/steampipe/.steampipe/internal/.passwd

RUN steampipe plugin install aws config code

RUN rm -rf /home/steampipe/.steampipe/config/*.spc

USER root

# expose postgres service default port
EXPOSE 9193

# expose dashboard service default port
EXPOSE 9194

# Use a constant workspace directory that can be mounted to
WORKDIR /workspace

COPY bin/check /opt/resource/check

COPY realcheck in out /opt/resource/

ENTRYPOINT [ "/opt/resource/check" ]