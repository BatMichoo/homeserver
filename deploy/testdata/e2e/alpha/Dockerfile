FROM busybox:1.36
COPY version /version
CMD ["sh", "-c", "if [ \"$(cat /version)\" != bad ]; then touch /tmp/ok; fi; exec sleep 86400"]
