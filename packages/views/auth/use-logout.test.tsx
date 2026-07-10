import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { workspaceKeys } from "@multica/core/workspace/queries";
import type { Workspace } from "@multica/core/types";

const mocks = vi.hoisted(() => ({
  authLogout: vi.fn(),
  clearWorkspaceStorage: vi.fn(),
  removeItem: vi.fn(),
  push: vi.fn(),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { logout: typeof mocks.authLogout }) => unknown) =>
    selector({ logout: mocks.authLogout }),
}));

vi.mock("@multica/core/platform", () => ({
  clearWorkspaceStorage: mocks.clearWorkspaceStorage,
  defaultStorage: { removeItem: mocks.removeItem },
}));

vi.mock("../navigation", () => ({
  useNavigation: () => ({ push: mocks.push }),
}));

import { useLogout } from "./use-logout";

function workspace(): Workspace {
  return { id: "ws-1", slug: "acme" } as Workspace;
}

describe("useLogout", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("keeps credentials and cache available until auth cleanup resolves", async () => {
    let finishLogout: (() => void) | undefined;
    mocks.authLogout.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finishLogout = resolve;
        }),
    );
    const queryClient = new QueryClient();
    queryClient.setQueryData(workspaceKeys.list(), [workspace()]);
    function Wrapper({ children }: { children: ReactNode }) {
      return (
        <QueryClientProvider client={queryClient}>
          {children}
        </QueryClientProvider>
      );
    }
    const { result } = renderHook(() => useLogout(), { wrapper: Wrapper });

    let logout: Promise<void> | undefined;
    act(() => {
      logout = result.current();
    });

    expect(mocks.authLogout).toHaveBeenCalledTimes(1);
    expect(queryClient.getQueryData(workspaceKeys.list())).toBeDefined();
    expect(mocks.push).not.toHaveBeenCalled();
    expect(mocks.clearWorkspaceStorage).toHaveBeenCalledWith(
      expect.anything(),
      "acme",
    );

    finishLogout?.();
    await act(async () => {
      await logout;
    });

    expect(queryClient.getQueryData(workspaceKeys.list())).toBeUndefined();
    expect(mocks.push).toHaveBeenCalledWith("/login");
  });
});
