FROM maven:3.9-eclipse-temurin-25 AS build
WORKDIR /src
COPY pom.xml .
RUN mvn -q dependency:go-offline
COPY src ./src
RUN mvn -q -DskipTests package

FROM eclipse-temurin:25-jre
WORKDIR /app
COPY --from=build /src/target/orders-api.jar /app/orders-api.jar
USER 10001:10001
EXPOSE 8080
# Exec form: java is PID 1 and receives SIGTERM directly, triggering graceful shutdown.
# 70% heap of 1 GiB leaves room for metaspace, thread stacks, and direct buffers.
ENTRYPOINT ["java", "-XX:MaxRAMPercentage=70", "-XX:+ExitOnOutOfMemoryError", "-jar", "/app/orders-api.jar"]
