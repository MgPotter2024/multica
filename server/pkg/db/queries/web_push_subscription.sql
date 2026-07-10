-- name: ListWebPushSubscriptionsByUser :many
SELECT * FROM web_push_subscription
WHERE user_id = $1
ORDER BY created_at ASC;

-- name: UpsertWebPushSubscription :one
INSERT INTO web_push_subscription (user_id, endpoint, p256dh, auth)
VALUES ($1, $2, $3, $4)
ON CONFLICT (endpoint) DO UPDATE SET
    user_id = EXCLUDED.user_id,
    p256dh = EXCLUDED.p256dh,
    auth = EXCLUDED.auth,
    updated_at = now()
RETURNING *;

-- name: DeleteWebPushSubscriptionByEndpoint :execrows
DELETE FROM web_push_subscription
WHERE user_id = $1 AND endpoint = $2;

-- name: DeleteWebPushSubscriptionByID :exec
DELETE FROM web_push_subscription
WHERE id = $1;
