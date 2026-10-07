FROM python:3.12-slim
WORKDIR /app
COPY service.py .
USER 65534:65534
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=8s CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8090/health', timeout=5)"
CMD ["python", "service.py"]
