export type NotificationGroupKey =
  | "assignments"
  | "status_changes"
  | "comments"
  | "updates"
  | "agent_activity"
  | "system_notifications";

export type NotificationGroupValue = "all" | "muted";

export type NotificationInboxMode = "all" | "mentions_only";

export type NotificationPreferences = Partial<
  Record<NotificationGroupKey, NotificationGroupValue>
> & {
  inbox_mode?: NotificationInboxMode;
};

export interface NotificationPreferenceResponse {
  workspace_id: string;
  preferences: NotificationPreferences;
}
