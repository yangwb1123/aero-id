import * as React from "react";

export interface AsyncResource<T> {
  data?: T;
  error?: Error;
  loading: boolean;
  reload(): void;
  replace(data: T): void;
}

export function useAsyncResource<T>(load: () => Promise<T>): AsyncResource<T> {
  const [data, setData] = React.useState<T>();
  const [error, setError] = React.useState<Error>();
  const [loading, setLoading] = React.useState(true);
  const [revision, setRevision] = React.useState(0);

  React.useEffect(() => {
    let active = true;
    setLoading(true);
    setError(undefined);
    load().then(
      (value) => {
        if (!active) return;
        setData(value);
        setLoading(false);
      },
      (reason: unknown) => {
        if (!active) return;
        setError(reason instanceof Error ? reason : new Error("请求失败"));
        setLoading(false);
      },
    );
    return () => {
      active = false;
    };
  }, [load, revision]);

  return {
    data,
    error,
    loading,
    reload: () => setRevision((value) => value + 1),
    replace: setData,
  };
}
