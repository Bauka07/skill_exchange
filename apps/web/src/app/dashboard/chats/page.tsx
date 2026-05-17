"use client";

import { useState, useEffect, useRef, useCallback } from "react";
import { useRouter } from "next/navigation";
import { Search, MessageSquare, Send } from "lucide-react";
import { clsx } from "clsx";
import { api } from "@/lib/api";
import type { ExchangeRequest, Message, Room } from "@/types/index";

// ─── Helper ───────────────────────────────────────────────────────────────────

function getContactName(contact: ExchangeRequest): string {
  return (
    contact.sender?.name ??
    contact.receiver?.name ??
    contact.from_user_profile?.name ??
    contact.to_user_profile?.name ??
    "Партнёр"
  );
}

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function ChatsPage() {
  const router = useRouter();

  const [contacts, setContacts] = useState<ExchangeRequest[]>([]);
  const [contactFilter, setContactFilter] = useState("");
  const [activeContact, setActiveContact] = useState<ExchangeRequest | null>(
    null,
  );
  const [messages, setMessages] = useState<Message[]>([]);
  const [messageInput, setMessageInput] = useState("");
  const [room, setRoom] = useState<Room | null>(null);
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);

  const scrollRef = useRef<HTMLDivElement>(null);

  // ── Auth guard ──────────────────────────────────────────────────────────────
  useEffect(() => {
    const token =
      typeof window !== "undefined"
        ? localStorage.getItem("access_token")
        : null;
    if (!token) {
      router.push("/auth");
    }
  }, [router]);

  // ── Load contacts + room on mount ───────────────────────────────────────────
  useEffect(() => {
    async function load() {
      setLoading(true);
      try {
        const [incoming, sent, roomData] = await Promise.all([
          api.getIncomingRequests(),
          api.getSentRequests(),
          // getMyRoom() throws a 404 when there is no active room — that is
          // expected behaviour, not a fatal error. Catch it so Promise.all
          // does not reject and the contacts list still loads correctly.
          api.getMyRoom().catch(() => null),
        ]);
        const accepted: ExchangeRequest[] = [
          ...(Array.isArray(incoming) ? incoming : []).filter(
            (r) => r.status === "accepted",
          ),
          ...(Array.isArray(sent) ? sent : []).filter(
            (r) => r.status === "accepted",
          ),
        ];
        setContacts(accepted);
        setRoom(roomData);
      } catch (err) {
        console.error("Failed to load chats:", err);
      } finally {
        setLoading(false);
      }
    }
    load();
  }, []);

  // ── Load messages when active contact changes ───────────────────────────────
  useEffect(() => {
    if (!activeContact || !room) return;
    api
      .getRoomMessages(room.id)
      .then(setMessages)
      .catch((err) => console.error("Failed to load messages:", err));
  }, [activeContact, room]);

  // ── Auto-scroll to bottom ───────────────────────────────────────────────────
  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [messages]);

  // ── Derived ─────────────────────────────────────────────────────────────────
  const filteredContacts = contacts.filter((c) =>
    getContactName(c).toLowerCase().includes(contactFilter.toLowerCase()),
  );

  // ── Send handler (optimistic UI — no REST send endpoint) ────────────────────
  const handleSend = useCallback(
    (e: React.FormEvent) => {
      e.preventDefault();
      const text = messageInput.trim();
      if (!text || !room || sending) return;

      setSending(true);
      const optimistic: Message = {
        id: `local-${Date.now()}`,
        sender: "me",
        content: text,
        timestamp: Date.now(),
      };
      setMessages((prev) => [...prev, optimistic]);
      setMessageInput("");
      setSending(false);
    },
    [messageInput, room, sending],
  );

  // ── Render ───────────────────────────────────────────────────────────────────
  return (
    <div className="flex h-full">
      {/* ── Left Pane: Contacts ─────────────────────────────────────────────── */}
      <div className="w-72 shrink-0 border-r border-white/5 flex flex-col bg-zinc-900/40">
        {/* Header + search */}
        <div className="p-4 border-b border-white/5">
          <h2 className="font-semibold text-zinc-100 mb-3">Чаты</h2>
          <div className="relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-zinc-500 pointer-events-none" />
            <input
              type="text"
              value={contactFilter}
              onChange={(e) => setContactFilter(e.target.value)}
              placeholder="Поиск контакта..."
              className="rounded-xl border border-white/5 bg-zinc-800/70 pl-9 pr-4 py-2 text-sm text-zinc-100 placeholder:text-zinc-500 w-full focus:border-blue-500/60 focus:outline-none"
            />
          </div>
        </div>

        {/* Contact list */}
        <div className="flex-1 overflow-y-auto p-2">
          {loading && (
            <div className="flex items-center justify-center h-24">
              <div className="w-5 h-5 border-2 border-blue-500 border-t-transparent rounded-full animate-spin" />
            </div>
          )}

          {!loading &&
            filteredContacts.map((contact) => {
              const name = getContactName(contact);
              const isActive = activeContact?.id === contact.id;
              return (
                <button
                  key={contact.id}
                  onClick={() => setActiveContact(contact)}
                  className={clsx(
                    "w-full flex items-center gap-3 px-3 py-3 rounded-xl text-left transition-colors",
                    isActive
                      ? "bg-blue-600/15 border border-blue-600/20"
                      : "hover:bg-white/5",
                  )}
                >
                  <div className="w-9 h-9 rounded-full bg-zinc-700 flex items-center justify-center text-zinc-300 text-sm font-bold shrink-0">
                    {name[0]?.toUpperCase() ?? "?"}
                  </div>
                  <div className="min-w-0">
                    <p className="text-sm font-medium text-zinc-100 truncate">
                      {name}
                    </p>
                    <p className="text-xs text-zinc-500 truncate">
                      Активный обмен
                    </p>
                  </div>
                </button>
              );
            })}

          {/* Empty state */}
          {!loading && contacts.length === 0 && (
            <div className="flex flex-col items-center justify-center h-40 text-center px-4">
              <MessageSquare className="w-8 h-8 text-zinc-600 mb-2" />
              <p className="text-sm text-zinc-500">Нет активных чатов</p>
              <p className="text-xs text-zinc-600 mt-1">
                Примите запрос на обмен, чтобы начать
              </p>
            </div>
          )}

          {/* Filtered but list non-empty — no results */}
          {!loading && contacts.length > 0 && filteredContacts.length === 0 && (
            <div className="flex flex-col items-center justify-center h-32 text-center px-4">
              <p className="text-sm text-zinc-500">Контакты не найдены</p>
            </div>
          )}
        </div>
      </div>

      {/* ── Right Pane: Chat Window ──────────────────────────────────────────── */}
      <div className="flex-1 flex flex-col min-w-0">
        {activeContact ? (
          <>
            {/* Chat header */}
            <div className="px-5 py-4 border-b border-white/5 flex items-center gap-3 shrink-0">
              <div className="w-9 h-9 rounded-full bg-zinc-700 flex items-center justify-center text-zinc-300 font-bold shrink-0">
                {getContactName(activeContact)[0]?.toUpperCase()}
              </div>
              <div>
                <h3 className="font-semibold text-zinc-100">
                  {getContactName(activeContact)}
                </h3>
                <p className="text-xs text-emerald-400">Активный обмен</p>
              </div>
            </div>

            {/* Messages area */}
            <div
              ref={scrollRef}
              className="flex-1 overflow-y-auto p-5 space-y-3"
            >
              {messages.map((msg, i) => {
                const isMe = msg.sender === "me";
                return (
                  <div
                    key={msg.id ?? i}
                    className={clsx(
                      "flex",
                      isMe ? "justify-end" : "justify-start",
                    )}
                  >
                    <div
                      className={clsx(
                        "max-w-[70%] min-w-0 rounded-2xl px-4 py-2.5 text-sm leading-relaxed break-words",
                        isMe
                          ? "bg-blue-600 text-white rounded-br-md"
                          : "bg-zinc-800 text-zinc-100 rounded-bl-md",
                      )}
                    >
                      <p>{msg.content}</p>
                      <span
                        className={clsx(
                          "block text-[10px] mt-1 opacity-50",
                          isMe ? "text-right" : "text-left",
                        )}
                      >
                        {new Date(
                          msg.timestamp < 1e12
                            ? msg.timestamp * 1000
                            : msg.timestamp,
                        ).toLocaleTimeString([], {
                          hour: "2-digit",
                          minute: "2-digit",
                        })}
                      </span>
                    </div>
                  </div>
                );
              })}

              {/* Empty messages, room exists */}
              {messages.length === 0 && room && (
                <div className="flex flex-col items-center justify-center h-full text-center gap-2 py-16">
                  <MessageSquare className="w-10 h-10 text-zinc-700" />
                  <p className="text-zinc-500 text-sm">Начните общение!</p>
                </div>
              )}

              {/* No room */}
              {!room && (
                <div className="flex items-center justify-center h-full">
                  <p className="text-zinc-600 text-sm">Комната не найдена</p>
                </div>
              )}
            </div>

            {/* Message input */}
            <form
              onSubmit={handleSend}
              className="p-4 border-t border-white/5 shrink-0"
            >
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  value={messageInput}
                  onChange={(e) => setMessageInput(e.target.value)}
                  placeholder={
                    room ? "Напишите сообщение..." : "Комната недоступна"
                  }
                  disabled={!room || sending}
                  className="flex-1 rounded-xl border border-white/5 bg-zinc-800/70 px-4 py-2.5 text-sm text-zinc-100 placeholder:text-zinc-500 focus:border-blue-500/60 focus:outline-none disabled:opacity-50"
                />
                <button
                  type="submit"
                  disabled={!room || !messageInput.trim() || sending}
                  className="p-2.5 rounded-xl bg-blue-600 text-white hover:bg-blue-500 disabled:opacity-40 transition-colors"
                >
                  <Send className="w-5 h-5" />
                </button>
              </div>
            </form>
          </>
        ) : (
          /* No contact selected */
          <div className="flex-1 flex flex-col items-center justify-center gap-3 text-center p-8">
            <div className="w-16 h-16 rounded-2xl bg-zinc-900 border border-white/5 flex items-center justify-center">
              <MessageSquare className="w-7 h-7 text-zinc-600" />
            </div>
            <h3 className="font-semibold text-zinc-300">Выберите чат</h3>
            <p className="text-sm text-zinc-500 max-w-xs">
              Выберите контакт слева, чтобы открыть историю переписки
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
