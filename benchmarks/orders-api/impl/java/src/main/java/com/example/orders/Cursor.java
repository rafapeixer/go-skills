package com.example.orders;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.time.OffsetDateTime;
import java.time.ZoneOffset;
import java.util.Base64;

/**
 * Keyset pagination position: the (created_at, id) of the last order returned.
 * Encoded as base64url("epochMicros:id"); Postgres timestamptz has microsecond precision, so this is lossless.
 */
public record Cursor(OffsetDateTime createdAt, long id) {

    public String encode() {
        Instant t = createdAt.toInstant();
        long micros = Math.addExact(Math.multiplyExact(t.getEpochSecond(), 1_000_000L), t.getNano() / 1_000);
        String raw = micros + ":" + id;
        return Base64.getUrlEncoder().withoutPadding().encodeToString(raw.getBytes(StandardCharsets.US_ASCII));
    }

    public static Cursor decode(String encoded) {
        try {
            String raw = new String(Base64.getUrlDecoder().decode(encoded), StandardCharsets.US_ASCII);
            int colon = raw.indexOf(':');
            if (colon < 0) {
                throw new IllegalArgumentException();
            }
            long micros = Long.parseLong(raw.substring(0, colon));
            long id = Long.parseLong(raw.substring(colon + 1));
            Instant t = Instant.ofEpochSecond(Math.floorDiv(micros, 1_000_000L), Math.floorMod(micros, 1_000_000L) * 1_000L);
            return new Cursor(OffsetDateTime.ofInstant(t, ZoneOffset.UTC), id);
        } catch (RuntimeException e) {
            throw ApiException.badRequest("invalid cursor");
        }
    }
}
