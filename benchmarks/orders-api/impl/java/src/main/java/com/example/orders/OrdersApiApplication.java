package com.example.orders;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.context.properties.EnableConfigurationProperties;

@SpringBootApplication
@EnableConfigurationProperties(OrdersProperties.class)
public class OrdersApiApplication {

    public static void main(String[] args) {
        SpringApplication.run(OrdersApiApplication.class, args);
    }
}
