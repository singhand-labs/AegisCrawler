import { Modal } from 'antd';

interface ConfirmModalProps {
  title: string;
  content: string;
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  confirmLoading?: boolean;
}

export default function ConfirmModal({
  title,
  content,
  open,
  onConfirm,
  onCancel,
  confirmLoading,
}: ConfirmModalProps) {
  return (
    <Modal
      title={title}
      open={open}
      onOk={onConfirm}
      onCancel={onCancel}
      confirmLoading={confirmLoading}
      okText="确认"
      cancelText="取消"
    >
      <p>{content}</p>
    </Modal>
  );
}
