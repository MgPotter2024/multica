export interface WebPushConfig {
  enabled: boolean;
  publicKey: string;
}

export interface WebPushSubscriptionInput {
  endpoint: string;
  keys: {
    p256dh: string;
    auth: string;
  };
}
